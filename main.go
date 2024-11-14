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

// checkWritePermission performs a thorough write permission test
func checkWritePermission(path string) error {
	// First check if we can create a directory
	testDir := filepath.Join(path, ".write_test_dir")
	if err := os.Mkdir(testDir, 0755); err != nil {
		return fmt.Errorf("cannot create directory: %v", err)
	}
	defer os.RemoveAll(testDir)

	// Then check if we can create a file
	testFile := filepath.Join(path, ".write_test")
	f, err := os.OpenFile(testFile, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return fmt.Errorf("cannot create file: %v", err)
	}

	// Try to write some data
	if _, err := f.WriteString("test"); err != nil {
		f.Close()
		os.Remove(testFile)
		return fmt.Errorf("cannot write to file: %v", err)
	}

	// Clean up
	f.Close()
	if err := os.Remove(testFile); err != nil {
		return fmt.Errorf("cannot remove test file: %v", err)
	}

	return nil
}

// checkMountedDirectoryPermissions verifies the mount point is writable after mounting
func checkMountedDirectoryPermissions(mountpoint string) error {
	log.Printf("Testing mounted directory permissions at %s...", mountpoint)

	// Wait a moment for the mount to stabilize
	time.Sleep(2 * time.Second)

	// Get the current state of the mount point
	info, err := os.Stat(mountpoint)
	if err != nil {
		return fmt.Errorf("failed to stat mounted directory: %v", err)
	}

	// Check ownership and permissions
	stat := info.Sys().(*syscall.Stat_t)
	currentUID := os.Getuid()

	log.Printf("Mounted directory owner: uid=%d (current uid=%d)", stat.Uid, currentUID)

	// Perform write test
	if err := checkWritePermission(mountpoint); err != nil {
		return fmt.Errorf("mounted directory is not writable: %v\n\n"+
			"The mount point is not writable by the current user. To fix this, unmount first then either:\n\n"+
			"1. Change ownership to current user (recommended):\n"+
			"   sudo chown %d:%d %s\n\n"+
			"2. OR allow all users to write (less secure):\n"+
			"   sudo chmod 777 %s\n\n"+
			"Then try mounting again.",
			err, currentUID, os.Getgid(), mountpoint, mountpoint)
	}

	return nil
}

// checkDirectoryPermissions checks if all required directories exist and are writable
func checkDirectoryPermissions(masterDir string, cacheDir string) error {
	log.Println("Checking directory permissions...")

	// Collect all directories that need checking
	dirs := map[string]string{
		"master": masterDir,
		"cache":  cacheDir,
	}

	var errors []string
	currentUID := os.Getuid()

	for name, dir := range dirs {
		if dir == "" {
			continue
		}

		log.Printf("Checking %s directory: %s", name, dir)

		// Get absolute path
		absPath, err := filepath.Abs(dir)
		if err != nil {
			errors = append(errors, fmt.Sprintf("Failed to get absolute path for %s directory: %v", name, err))
			continue
		}

		// Check if directory exists
		info, err := os.Stat(absPath)
		if os.IsNotExist(err) {
			errors = append(errors, fmt.Sprintf("%s directory does not exist: %s\nRun: mkdir -p %s", name, absPath, absPath))
			continue
		} else if err != nil {
			errors = append(errors, fmt.Sprintf("Failed to check %s directory: %v", name, err))
			continue
		}

		// Check ownership
		if stat, ok := info.Sys().(*syscall.Stat_t); ok {
			log.Printf("%s directory owner: uid=%d (current uid=%d)", name, stat.Uid, currentUID)
			if int(stat.Uid) == 0 { // root owned
				errors = append(errors, fmt.Sprintf("%s directory is owned by root: %s\nRun: sudo chown %d:%d %s",
					name, absPath, currentUID, os.Getgid(), absPath))
				continue
			}
		}

		// Check write permission with thorough test
		log.Printf("Testing write permissions for %s directory...", name)
		if err := checkWritePermission(absPath); err != nil {
			log.Printf("Write permission test failed for %s: %v", name, err)
			errors = append(errors, fmt.Sprintf("%s directory is not writable: %s\nError: %v\n\nTo fix, run either:\n"+
				"1. sudo chown %d:%d %s\n"+
				"2. sudo chmod 777 %s",
				name, absPath, err, currentUID, os.Getgid(), absPath, absPath))
			continue
		}
		log.Printf("Write permission test passed for %s directory", name)
	}

	if len(errors) > 0 {
		return fmt.Errorf("permission checks failed. Please fix the following issues:\n\n%s", strings.Join(errors, "\n\n"))
	}

	log.Println("All directory permission checks passed successfully")
	return nil
}

func checkFuseRequirements() error {
	log.Println("Checking FUSE requirements...")

	// Check for fusermount3
	if _, err := exec.LookPath("fusermount3"); err != nil {
		return fmt.Errorf("FUSE3 tools not found - please install them using:\n" +
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

	log.Println("FUSE requirements check passed")
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
	// Ensure proper permissions on mount point
	if err := os.Chmod(mountpoint, 0755); err != nil {
		return fmt.Errorf("failed to set mount point permissions: %v", err)
	}

	c, err := fuse.Mount(
		mountpoint,
		fuse.FSName("remotefs"),
		fuse.Subtype("remotefs"),
		fuse.AllowOther(),
		fuse.DefaultPermissions(),
		fuse.WritebackCache(),
		fuse.MaxReadahead(128*1024),
	)
	if err != nil {
		return fmt.Errorf("mount failed: %v", err)
	}

	// Check mounted directory permissions
	if err := checkMountedDirectoryPermissions(mountpoint); err != nil {
		c.Close()
		cleanup(mountpoint)
		return err
	}

	go func() {
		<-done
		c.Close()
	}()

	filesys := &FS{
		client: &http.Client{
			Timeout: 30 * time.Second, // Increased timeout
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

		mountpoint = config.Mount
		serverAddr = config.ServerAddr

		// Check directory permissions (except mount point) before proceeding
		if err := checkDirectoryPermissions(masterDir, cacheDir); err != nil {
			log.Fatalf("Permission check failed:\n\n%v", err)
		}

		// Check FUSE requirements before proceeding
		if err := checkFuseRequirements(); err != nil {
			log.Fatalf("FUSE check failed:\n\n%v", err)
		}

		filesystems, err := createFileSystems(config)
		if err != nil {
			log.Fatalf("Error creating filesystems: %v", err)
		}

		fs = NewChainFS(filesystems)
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

		// Check directory permissions (except mount point) before proceeding
		if err := checkDirectoryPermissions(masterDir, cacheDir); err != nil {
			log.Fatalf("Permission check failed:\n\n%v", err)
		}

		// Check FUSE requirements before proceeding
		if err := checkFuseRequirements(); err != nil {
			log.Fatalf("FUSE check failed:\n\n%v", err)
		}

		fs, err = NewLocalFS(FileSystemConfig{
			Role:    fsRole,
			MaxSize: maxCacheSize,
			Features: FileSystemFeatures{
				CanUpdate: true,
				CanDelete: true,
				CanLock:   true, // Enable locking
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
