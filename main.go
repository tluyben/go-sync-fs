package main

import (
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

type FileInfo struct {
	Name    string
	Size    int64
	Mode    os.FileMode
	ModTime time.Time
	IsDir   bool
	Content []byte // Only for files
}

type FileServer struct {
	root string
}

func (s *FileServer) handleInfo(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	fullPath := filepath.Join(s.root, path)

	info, err := os.Stat(fullPath)
	if os.IsNotExist(err) {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	fileInfo := FileInfo{
		Name:    info.Name(),
		Size:    info.Size(),
		Mode:    info.Mode(),
		ModTime: info.ModTime(),
		IsDir:   info.IsDir(),
	}

	json.NewEncoder(w).Encode(fileInfo)
}

func (s *FileServer) handleList(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	fullPath := filepath.Join(s.root, path)

	dir, err := os.Open(fullPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer dir.Close()

	entries, err := dir.Readdir(-1)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	var files []FileInfo
	for _, entry := range entries {
		files = append(files, FileInfo{
			Name:    entry.Name(),
			Size:    entry.Size(),
			Mode:    entry.Mode(),
			ModTime: entry.ModTime(),
			IsDir:   entry.IsDir(),
		})
	}

	json.NewEncoder(w).Encode(files)
}

func (s *FileServer) handleRead(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	fullPath := filepath.Join(s.root, path)

	info, err := os.Stat(fullPath)
	if os.IsNotExist(err) {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	content, err := os.ReadFile(fullPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	fileInfo := FileInfo{
		Name:    info.Name(),
		Size:    info.Size(),
		Mode:    info.Mode(),
		ModTime: info.ModTime(),
		IsDir:   info.IsDir(),
		Content: content,
	}

	json.NewEncoder(w).Encode(fileInfo)
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
	fullPath := filepath.Join(s.root, path)

	// Ensure parent directory exists
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if err := os.WriteFile(fullPath, fileInfo.Content, fileInfo.Mode); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

func main() {
	var root string
	flag.StringVar(&root, "root", "", "Root directory to serve")
	var addr string
	flag.StringVar(&addr, "addr", ":8080", "Address to listen on")
	flag.Parse()

	if root == "" {
		log.Fatal("Must specify -root")
	}

	absRoot, err := filepath.Abs(root)
	if err != nil {
		log.Fatal(err)
	}

	server := &FileServer{root: absRoot}

	http.HandleFunc("/info", server.handleInfo)
	http.HandleFunc("/list", server.handleList)
	http.HandleFunc("/read", server.handleRead)
	http.HandleFunc("/write", server.handleWrite)

	log.Printf("Serving %s on %s", absRoot, addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}