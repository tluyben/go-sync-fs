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
	attr.Mode = os.ModeDir | 0o775
	attr.Uid = uint32(os.Getuid()) // Set owner to current user
	attr.Gid = uint32(os.Getgid()) // Set group to current user's group
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
	attr.Uid = uint32(os.Getuid())
	attr.Gid = uint32(os.Getgid())
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
	// First read the entire file
	content, err := h.file.ReadAll(ctx)
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	// If the offset is beyond the current file size, pad with zeros
	if req.Offset > int64(len(content)) {
		newContent := make([]byte, req.Offset)
		copy(newContent, content)
		content = newContent
	}

	// Ensure the slice is large enough to hold the write
	writeEnd := req.Offset + int64(len(req.Data))
	if writeEnd > int64(len(content)) {
		newContent := make([]byte, writeEnd)
		copy(newContent, content)
		content = newContent
	}

	// Copy the new data at the correct offset
	copy(content[req.Offset:], req.Data)

	// Create FileInfo for the write request
	fileInfo := FileInfo{
		Content: content,
		Mode:    h.file.info.Mode,
	}

	// Convert to JSON
	data, err := json.Marshal(fileInfo)
	if err != nil {
		return err
	}

	// Send write request to server
	httpResp, err := h.file.fs.client.Post(
		fmt.Sprintf("%s/write?path=%s", h.file.fs.baseURL, h.file.path),
		"application/json",
		bytes.NewReader(data),
	)
	if err != nil {
		return err
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode != http.StatusOK {
		return syscall.EIO
	}

	resp.Size = len(req.Data)
	h.file.info.Size = writeEnd
	return nil
}

func (d *Dir) Create(ctx context.Context, req *fuse.CreateRequest, resp *fuse.CreateResponse) (fs.Node, fs.Handle, error) {
	path := filepath.Join(d.path, req.Name)

	// Create empty file through API
	fileInfo := FileInfo{
		Content: []byte{},
		Mode:    req.Mode,
	}

	data, err := json.Marshal(fileInfo)
	if err != nil {
		return nil, nil, err
	}

	httpResp, err := d.fs.client.Post(
		fmt.Sprintf("%s/write?path=%s", d.fs.baseURL, path),
		"application/json",
		bytes.NewReader(data),
	)
	if err != nil {
		return nil, nil, err
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode != http.StatusOK {
		return nil, nil, syscall.EIO
	}

	// Create the file node
	f := &File{
		fs:   d.fs,
		path: path,
		info: FileInfo{
			Name:    req.Name,
			Mode:    req.Mode,
			ModTime: time.Now(),
		},
	}

	// Create the handle with appropriate lock type
	var lockType LockType
	if req.Flags.IsReadOnly() {
		lockType = ReadLock
	} else if req.Flags.IsWriteOnly() {
		lockType = WriteLock
	} else if req.Flags.IsReadWrite() {
		lockType = ExclusiveLock
	}

	h := &FileHandle{
		file:     f,
		lockType: lockType,
	}

	// Set proper response flags for write access
	resp.OpenResponse.Flags = fuse.OpenResponseFlags(req.Flags)

	return f, h, nil
}

func (f *File) SetAttr(ctx context.Context, req *fuse.SetattrRequest, resp *fuse.SetattrResponse) error {
	if req.Valid.Mode() {
		// Create FileInfo with new mode
		fileInfo := FileInfo{
			Mode: req.Mode,
		}

		data, err := json.Marshal(fileInfo)
		if err != nil {
			return err
		}

		// Send mode update to server
		httpResp, err := f.fs.client.Post(
			fmt.Sprintf("%s/write?path=%s", f.fs.baseURL, f.path),
			"application/json",
			bytes.NewReader(data),
		)
		if err != nil {
			return err
		}
		defer httpResp.Body.Close()

		if httpResp.StatusCode != http.StatusOK {
			return syscall.EIO
		}

		f.info.Mode = req.Mode
	}

	if req.Valid.Size() {
		// Handle truncate - convert uint64 to int64
		size := int64(req.Size) // explicit conversion
		fileInfo := FileInfo{
			Content: make([]byte, size),
			Mode:    f.info.Mode,
		}

		data, err := json.Marshal(fileInfo)
		if err != nil {
			return err
		}

		httpResp, err := f.fs.client.Post(
			fmt.Sprintf("%s/write?path=%s", f.fs.baseURL, f.path),
			"application/json",
			bytes.NewReader(data),
		)
		if err != nil {
			return err
		}
		defer httpResp.Body.Close()

		if httpResp.StatusCode != http.StatusOK {
			return syscall.EIO
		}

		f.info.Size = size
	}

	// Update response attributes
	resp.Attr = fuse.Attr{
		Mode:  f.info.Mode,
		Size:  uint64(f.info.Size),
		Mtime: f.info.ModTime,
		Uid:   uint32(os.Getuid()),
		Gid:   uint32(os.Getgid()),
	}

	return nil
}
