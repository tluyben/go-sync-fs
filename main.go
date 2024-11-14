package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
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

func checkDirectoryRequirements(mountpoint string, masterDir string, cacheDir string) error {
	// Check if directories exist and create them if necessary
	dirs := map[string]string{
		"mount":  mountpoint,
		"master": masterDir,
		"cache":  cacheDir,
	}

	for name, dir := range dirs {
		if dir == "" {
			continue
		}

		// Get absolute path
		absPath, err := filepath.Abs(dir)
		if err != nil {
			return fmt.Errorf("failed to get absolute path for %s directory: %v", name, err)
		}

		// Check if directory exists
		info, err := os.Stat(absPath)
		if os.IsNotExist(err) {
			// Create directory with proper permissions
			if err := os.MkdirAll(absPath, 0755); err != nil {
				return fmt.Errorf("failed to create %s directory: %v\nPlease run: mkdir -p %s && chmod 755 %s",
					name, err, absPath, absPath)
			}
		} else if err != nil {
			return fmt.Errorf("failed to check %s directory: %v", name, err)
		} else {
			// Directory exists, check permissions
			mode := info.Mode()
			if mode.Perm()&0755 != 0755 {
				return fmt.Errorf("%s directory has incorrect permissions %v\nPlease run: chmod 755 %s",
					name, mode.Perm(), absPath)
			}
		}

		// Check if directory is writable
		testFile := filepath.Join(absPath, ".write_test")
		f, err := os.Create(testFile)
		if err != nil {
			return fmt.Errorf("%s directory is not writable: %v\nPlease run: chmod u+w %s",
				name, err, absPath)
		}
		f.Close()
		os.Remove(testFile)
	}

	return nil
}

func checkFuseRequirements() error {
	// Check for fusermount3
	if _, err := exec.LookPath("fusermount3"); err != nil {
		return fmt.Errorf("FUSE3 tools not found. Please install them using:\n" +
			"For Debian/Ubuntu: sudo apt install -y fuse3\n" +
			"For Fedora: sudo dnf install -y fuse3\n" +
			"For Arch Linux: sudo pacman -S fuse3\n")
	}

	// Check for user_allow_other in /etc/fuse.conf
	file, err := os.Open("/etc/fuse.conf")
	if err != nil {
		return fmt.Errorf("could not open /etc/fuse.conf: %v\n"+
			"Please create the file and add 'user_allow_other' to enable the allow_other mount option", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	userAllowOtherFound := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "user_allow_other" {
			userAllowOtherFound = true
			break
		}
	}

	if !userAllowOtherFound {
		return fmt.Errorf("'user_allow_other' not found in /etc/fuse.conf\n" +
			"Please add 'user_allow_other' to /etc/fuse.conf using:\n" +
			"echo 'user_allow_other' | sudo tee -a /etc/fuse.conf")
	}

	return nil
}

func cleanup(mountpoint string) {
	log.Printf("Cleaning up mount at %s", mountpoint)
	cmd := exec.Command("fusermount3", "-u", mountpoint)
	if err := cmd.Run(); err != nil {
		log.Printf("Error unmounting: %v", err)
	}
}

func startFUSE(mountpoint string, serverURL string, done chan struct{}) error {
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

	go func() {
		<-done
		c.Close()
	}()

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
	var cacheDir string

	if configPath != "" {
		// Use YAML config
		config, err := LoadConfig(configPath)
		if err != nil {
			log.Fatalf("Error loading config: %v", err)
		}

		// Check directory requirements before creating filesystems
		for _, fsConfig := range config.FileSystems {
			if FileSystemRole(fsConfig.Role) == RoleCache {
				cacheDir = fsConfig.Path
			} else if FileSystemRole(fsConfig.Role) == RoleMain {
				masterDir = fsConfig.Path
			}
		}

		if err := checkDirectoryRequirements(config.Mount, masterDir, cacheDir); err != nil {
			log.Fatalf("Directory requirements check failed: %v", err)
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

		// For legacy mode, if role is cache, use cache directory
		if fsRole == RoleCache {
			cacheDir = masterDir
		}

		if err := checkDirectoryRequirements(mountpoint, masterDir, cacheDir); err != nil {
			log.Fatalf("Directory requirements check failed: %v", err)
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

	// Set up signal handling
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	done := make(chan struct{})

	// Start the file server in a goroutine
	go func() {
		if err := startFileServer(fs, serverAddr); err != nil {
			log.Printf("File server error: %v", err)
			close(done)
		}
	}()

	// Give the server a moment to start
	time.Sleep(100 * time.Millisecond)

	// Construct server URL for FUSE
	serverURL := fmt.Sprintf("http://localhost%s", serverAddr)
	if serverAddr[0] != ':' {
		serverURL = fmt.Sprintf("http://%s", serverAddr)
	}

	// Handle signals in a goroutine
	go func() {
		sig := <-sigChan
		log.Printf("Received signal: %v", sig)
		cleanup(mountpoint)
		close(done)
		os.Exit(0)
	}()

	// Start FUSE
	if err := startFUSE(mountpoint, serverURL, done); err != nil {
		cleanup(mountpoint)
		log.Fatal(err)
	}
}
