package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"time"

	"bazil.org/fuse"
	"bazil.org/fuse/fs"
)

// Server components
type FileServer struct {
	fs ServerFS
}

func (s *FileServer) handleInfo(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")

	info, err := s.fs.Info(path)
	if os.IsNotExist(err) {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	json.NewEncoder(w).Encode(info)
}

func (s *FileServer) handleList(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")

	files, err := s.fs.List(path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	json.NewEncoder(w).Encode(files)
}

func (s *FileServer) handleRead(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")

	info, err := s.fs.Info(path)
	if os.IsNotExist(err) {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	content, err := s.fs.Read(path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	info.Content = content
	json.NewEncoder(w).Encode(info)
}

func (s *FileServer) handleWrite(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var fileInfo FileInfo
	if err := json.NewDecoder(r.Body).Decode(&fileInfo); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	path := r.URL.Query().Get("path")

	if err := s.fs.Write(path, fileInfo.Content, fileInfo.Mode); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

func startFileServer(fs ServerFS, serverAddr string) error {
	server := &FileServer{fs: fs}

	http.HandleFunc("/info", server.handleInfo)
	http.HandleFunc("/list", server.handleList)
	http.HandleFunc("/read", server.handleRead)
	http.HandleFunc("/write", server.handleWrite)

	log.Printf("Starting server on %s", serverAddr)
	return http.ListenAndServe(serverAddr, nil)
}

func checkFuseRequirements() error {
	if _, err := exec.LookPath("fusermount3"); err != nil {
		return fmt.Errorf("FUSE3 tools not found. Please install them using:\n" +
			"For Debian/Ubuntu: sudo apt install -y fuse3\n" +
			"For Fedora: sudo dnf install -y fuse3\n" +
			"For Arch Linux: sudo pacman -S fuse3\n")
	}
	return nil
}

func startFUSE(mountpoint string, serverURL string) error {
	if err := checkFuseRequirements(); err != nil {
		return err
	}

	c, err := fuse.Mount(
		mountpoint,
		fuse.FSName("remotefs"),
		fuse.Subtype("remotefs"),
		fuse.AllowOther(),
	)
	if err != nil {
		return err
	}
	defer c.Close()

	filesys := &FS{
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
		baseURL: serverURL,
	}

	log.Printf("Mounting FUSE at %s, connecting to %s", mountpoint, serverURL)
	return fs.Serve(c, filesys)
}

func main() {
	var configPath string
	var masterDir string
	var serverAddr string
	var mountpoint string
	var role string
	var maxCacheSize int64

	// Support both config file and command line arguments
	flag.StringVar(&configPath, "config", "", "Path to YAML config file")
	flag.StringVar(&masterDir, "master", "", "Master directory to serve files from (legacy)")
	flag.StringVar(&serverAddr, "server", ":8080", "Server address (host:port) (legacy)")
	flag.StringVar(&mountpoint, "mount", "", "Directory to mount FUSE filesystem (legacy)")
	flag.StringVar(&role, "role", "main", "Filesystem role (main or cache) (legacy)")
	flag.Int64Var(&maxCacheSize, "cache-size", 1024*1024*1024, "Max cache size in bytes (default 1GB) (legacy)")
	flag.Parse()

	var fs ServerFS
	var err error

	if configPath != "" {
		// Use YAML config
		config, err := LoadConfig(configPath)
		if err != nil {
			log.Fatalf("Error loading config: %v", err)
		}

		filesystems, err := createFileSystems(config)
		if err != nil {
			log.Fatalf("Error creating filesystems: %v", err)
		}

		fs = NewChainFS(filesystems)
		serverAddr = config.ServerAddr
		mountpoint = config.Mount
	} else {
		// Legacy command line arguments
		if masterDir == "" {
			log.Fatal("Must specify -master or provide a config file with -config")
		}
		if mountpoint == "" {
			log.Fatal("Must specify -mount or provide a config file with -config")
		}

		fsRole := FileSystemRole(role)
		if fsRole != RoleMain && fsRole != RoleCache {
			log.Fatal("Role must be either 'main' or 'cache'")
		}

		fs, err = NewLocalFS(FileSystemConfig{
			Role:    fsRole,
			MaxSize: maxCacheSize,
			Features: FileSystemFeatures{
				CanUpdate: true,
				CanDelete: true,
				CanLock:   false,
			},
			RootPath: masterDir,
		})
		if err != nil {
			log.Fatal(err)
		}
	}

	// Start the file server in a goroutine
	go func() {
		if err := startFileServer(fs, serverAddr); err != nil {
			log.Fatal(err)
		}
	}()

	// Give the server a moment to start
	time.Sleep(100 * time.Millisecond)

	// Construct server URL for FUSE
	serverURL := fmt.Sprintf("http://localhost%s", serverAddr)
	if serverAddr[0] != ':' {
		serverURL = fmt.Sprintf("http://%s", serverAddr)
	}

	// Start FUSE
	if err := startFUSE(mountpoint, serverURL); err != nil {
		log.Fatal(err)
	}
}
