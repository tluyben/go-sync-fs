package main

import (
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
