package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"bazil.org/fuse"
	"bazil.org/fuse/fs"
	_ "bazil.org/fuse/fs/fstestutil"
)

type FileInfo struct {
	Name    string
	Size    int64
	Mode    os.FileMode
	ModTime time.Time
	IsDir   bool
	Content []byte // Only for files
}

type FS struct {
	client  *http.Client
	baseURL string
}

func (fs *FS) Root() (fs.Node, error) {
	return &Dir{
		fs:   fs,
		path: "/",
	}, nil
}

type Dir struct {
	fs   *FS
	path string
}

func (d *Dir) Attr(ctx context.Context, attr *fuse.Attr) error {
	attr.Mode = os.ModeDir | 0o755
	attr.Mtime = time.Now()
	return nil
}

func (d *Dir) Lookup(ctx context.Context, name string) (fs.Node, error) {
	path := filepath.Join(d.path, name)

	resp, err := d.fs.client.Get(fmt.Sprintf("%s/info?path=%s", d.fs.baseURL, path))
	if err != nil {
		return nil, syscall.ENOENT
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, syscall.ENOENT
	}

	var info FileInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, err
	}

	if info.IsDir {
		return &Dir{fs: d.fs, path: path}, nil
	}
	return &File{fs: d.fs, path: path, info: info}, nil
}

func (d *Dir) ReadDirAll(ctx context.Context) ([]fuse.Dirent, error) {
	resp, err := d.fs.client.Get(fmt.Sprintf("%s/list?path=%s", d.fs.baseURL, d.path))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var files []FileInfo
	if err := json.NewDecoder(resp.Body).Decode(&files); err != nil {
		return nil, err
	}

	var dirDirs []fuse.Dirent
	for _, f := range files {
		var dtype fuse.DirentType
		if f.IsDir {
			dtype = fuse.DT_Dir
		} else {
			dtype = fuse.DT_File
		}
		dirDirs = append(dirDirs, fuse.Dirent{
			Name: f.Name,
			Type: dtype,
		})
	}
	return dirDirs, nil
}

type File struct {
	fs   *FS
	path string
	info FileInfo
}

func (f *File) Attr(ctx context.Context, attr *fuse.Attr) error {
	attr.Mode = f.info.Mode
	attr.Size = uint64(f.info.Size)
	attr.Mtime = f.info.ModTime
	return nil
}

func (f *File) ReadAll(ctx context.Context) ([]byte, error) {
	resp, err := f.fs.client.Get(fmt.Sprintf("%s/read?path=%s", f.fs.baseURL, f.path))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var fileData FileInfo
	if err := json.NewDecoder(resp.Body).Decode(&fileData); err != nil {
		return nil, err
	}

	return fileData.Content, nil
}

// Implement file locking through FUSE

func (f *File) Open(ctx context.Context, req *fuse.OpenRequest, resp *fuse.OpenResponse) (fs.Handle, error) {
	// Check the flags to determine the type of access
	flags := req.Flags

	var lockType LockType
	if flags.IsReadOnly() {
		lockType = ReadLock
	} else if flags.IsWriteOnly() {
		lockType = WriteLock
	} else if flags.IsReadWrite() {
		lockType = ExclusiveLock
	}

	// Try to acquire the lock
	httpResp, err := f.fs.client.Post(fmt.Sprintf("%s/lock?path=%s&type=%d&pid=%d",
		f.fs.baseURL,
		f.path,
		lockType,
		os.Getpid()),
		"application/json",
		nil)
	if err != nil {
		return nil, err
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode != http.StatusOK {
		return nil, syscall.EACCES
	}

	handle := &FileHandle{file: f, lockType: lockType}
	return handle, nil
}

type FileHandle struct {
	file     *File
	lockType LockType
}

func (h *FileHandle) Release(ctx context.Context, req *fuse.ReleaseRequest) error {
	// Release the lock
	httpResp, err := h.file.fs.client.Post(fmt.Sprintf("%s/unlock?path=%s&pid=%d",
		h.file.fs.baseURL,
		h.file.path,
		os.Getpid()),
		"application/json",
		nil)
	if err != nil {
		return err
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode != http.StatusOK {
		return syscall.EACCES
	}

	return nil
}

func (h *FileHandle) Read(ctx context.Context, req *fuse.ReadRequest, resp *fuse.ReadResponse) error {
	content, err := h.file.ReadAll(ctx)
	if err != nil {
		return err
	}

	if req.Offset > int64(len(content)) {
		return nil
	}

	end := req.Offset + int64(req.Size)
	if end > int64(len(content)) {
		end = int64(len(content))
	}

	resp.Data = content[req.Offset:end]
	return nil
}

func (h *FileHandle) Write(ctx context.Context, req *fuse.WriteRequest, resp *fuse.WriteResponse) error {
	// Send write request to server
	httpResp, err := h.file.fs.client.Post(fmt.Sprintf("%s/write?path=%s",
		h.file.fs.baseURL,
		h.file.path),
		"application/octet-stream",
		bytes.NewReader(req.Data))
	if err != nil {
		return err
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode != http.StatusOK {
		return syscall.EIO
	}

	resp.Size = len(req.Data)
	return nil
}
