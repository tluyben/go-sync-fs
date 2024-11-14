package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// FileSystemFeatures represents the capabilities of a filesystem
type FileSystemFeatures struct {
	CanUpdate bool
	CanDelete bool
	CanLock   bool
}

// FileSystemRole defines the role of the filesystem
type FileSystemRole string

const (
	RoleMain  FileSystemRole = "main"
	RoleCache FileSystemRole = "cache"
)

// LockType represents the type of lock
type LockType int

const (
	ReadLock LockType = iota
	WriteLock
	ExclusiveLock
)

// FileLock represents a lock on a file
type FileLock struct {
	Path      string
	LockType  LockType
	CreatedAt time.Time
	ProcessID int
}

// FileSystemConfig holds the configuration for a filesystem
type FileSystemConfig struct {
	Role     FileSystemRole
	MaxSize  int64 // bytes, only used for cache role
	Features FileSystemFeatures
	RootPath string
}

// ServerFS defines the interface that all filesystem implementations must satisfy
type ServerFS interface {
	// Basic operations
	Info(path string) (FileInfo, error)
	List(path string) ([]FileInfo, error)
	Read(path string) ([]byte, error)
	Write(path string, content []byte, mode os.FileMode) error
	Delete(path string) error

	// Lock operations
	Lock(path string, lockType LockType, processID int) error
	Unlock(path string, processID int) error
	IsLocked(path string) (bool, LockType, error)

	// Metadata
	GetFeatures() FileSystemFeatures
	GetRole() FileSystemRole
	GetUsage() (int64, error)
}

// CacheEntry represents an entry in the cache
type CacheEntry struct {
	Path     string
	Size     int64
	LastUsed time.Time
}

// LocalFS implements ServerFS for a local filesystem
type LocalFS struct {
	config    FileSystemConfig
	root      string
	mutex     sync.RWMutex
	cacheList []CacheEntry // Only used when role is RoleCache
	locks     map[string]FileLock
	lockMutex sync.RWMutex
}

// NewLocalFS creates a new LocalFS instance
func NewLocalFS(config FileSystemConfig) (*LocalFS, error) {
	if config.Role == RoleCache && config.MaxSize <= 0 {
		return nil, errors.New("cache filesystem requires positive MaxSize")
	}

	absRoot, err := filepath.Abs(config.RootPath)
	if err != nil {
		return nil, err
	}

	// Ensure the root directory exists
	if err := os.MkdirAll(absRoot, 0755); err != nil {
		return nil, err
	}

	return &LocalFS{
		config:    config,
		root:      absRoot,
		cacheList: make([]CacheEntry, 0),
		locks:     make(map[string]FileLock),
	}, nil
}

// Lock implements file locking
func (l *LocalFS) Lock(path string, lockType LockType, processID int) error {
	if !l.config.Features.CanLock {
		return errors.New("filesystem does not support locking")
	}

	l.lockMutex.Lock()
	defer l.lockMutex.Unlock()

	// Check if file exists
	fullPath := filepath.Join(l.root, path)
	if _, err := os.Stat(fullPath); err != nil {
		return err
	}

	// Check existing lock
	if existingLock, exists := l.locks[path]; exists {
		// Allow multiple read locks
		if existingLock.LockType == ReadLock && lockType == ReadLock {
			return nil
		}
		return errors.New("file is already locked")
	}

	// Create new lock
	l.locks[path] = FileLock{
		Path:      path,
		LockType:  lockType,
		CreatedAt: time.Now(),
		ProcessID: processID,
	}

	return nil
}

// Unlock removes a lock on a file
func (l *LocalFS) Unlock(path string, processID int) error {
	if !l.config.Features.CanLock {
		return errors.New("filesystem does not support locking")
	}

	l.lockMutex.Lock()
	defer l.lockMutex.Unlock()

	lock, exists := l.locks[path]
	if !exists {
		return errors.New("file is not locked")
	}

	if lock.ProcessID != processID {
		return errors.New("lock belongs to different process")
	}

	delete(l.locks, path)
	return nil
}

// IsLocked checks if a file is locked
func (l *LocalFS) IsLocked(path string) (bool, LockType, error) {
	if !l.config.Features.CanLock {
		return false, 0, errors.New("filesystem does not support locking")
	}

	l.lockMutex.RLock()
	defer l.lockMutex.RUnlock()

	if lock, exists := l.locks[path]; exists {
		return true, lock.LockType, nil
	}

	return false, 0, nil
}

func (l *LocalFS) Info(path string) (FileInfo, error) {
	fullPath := filepath.Join(l.root, path)
	info, err := os.Stat(fullPath)
	if err != nil {
		return FileInfo{}, err
	}

	return FileInfo{
		Name:    info.Name(),
		Size:    info.Size(),
		Mode:    info.Mode(),
		ModTime: info.ModTime(),
		IsDir:   info.IsDir(),
	}, nil
}

func (l *LocalFS) List(path string) ([]FileInfo, error) {
	fullPath := filepath.Join(l.root, path)

	entries, err := os.ReadDir(fullPath)
	if err != nil {
		return nil, err
	}

	var files []FileInfo
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}

		files = append(files, FileInfo{
			Name:    info.Name(),
			Size:    info.Size(),
			Mode:    info.Mode(),
			ModTime: info.ModTime(),
			IsDir:   info.IsDir(),
		})
	}

	return files, nil
}

func (l *LocalFS) Read(path string) ([]byte, error) {
	// Check read lock
	if l.config.Features.CanLock {
		locked, lockType, _ := l.IsLocked(path)
		if locked && (lockType == WriteLock || lockType == ExclusiveLock) {
			return nil, errors.New("file is locked for writing")
		}
	}

	fullPath := filepath.Join(l.root, path)
	content, err := os.ReadFile(fullPath)
	if err != nil {
		return nil, err
	}

	if l.config.Role == RoleCache {
		l.updateCacheEntry(path, int64(len(content)))
	}

	return content, nil
}

func (l *LocalFS) Write(path string, content []byte, mode os.FileMode) error {
	if !l.config.Features.CanUpdate {
		return errors.New("filesystem does not support updates")
	}

	// Check write lock
	if l.config.Features.CanLock {
		if lock, exists := l.locks[path]; exists {
			// Allow write if the process has a write or exclusive lock
			if lock.ProcessID == os.Getpid() && (lock.LockType == WriteLock || lock.LockType == ExclusiveLock) {
				// Process has appropriate lock, allow write
			} else if lock.LockType == ReadLock {
				return errors.New("file is locked for reading")
			} else {
				return errors.New("file is locked by another process")
			}
		}
	}

	fullPath := filepath.Join(l.root, path)

	// Ensure parent directory exists with proper permissions
	if err := os.MkdirAll(filepath.Dir(fullPath), 0775); err != nil {
		return fmt.Errorf("failed to create directory: %v", err)
	}

	if l.config.Role == RoleCache {
		// Check if we need to make space in the cache
		if err := l.ensureCacheSpace(int64(len(content))); err != nil {
			return err
		}
	}

	// Create or truncate the file with proper permissions
	f, err := os.OpenFile(fullPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return fmt.Errorf("failed to open file for writing: %v", err)
	}
	defer f.Close()

	// Write the content
	if _, err := f.Write(content); err != nil {
		return fmt.Errorf("failed to write content: %v", err)
	}

	// Ensure the file has the correct permissions
	if err := f.Chmod(mode); err != nil {
		return fmt.Errorf("failed to set file permissions: %v", err)
	}

	if l.config.Role == RoleCache {
		l.updateCacheEntry(path, int64(len(content)))
	}

	return nil
}

func (l *LocalFS) Delete(path string) error {
	if !l.config.Features.CanDelete {
		return errors.New("filesystem does not support deletion")
	}

	// Check exclusive lock
	if l.config.Features.CanLock {
		locked, _, _ := l.IsLocked(path)
		if locked {
			return errors.New("file is locked")
		}
	}

	fullPath := filepath.Join(l.root, path)
	if err := os.Remove(fullPath); err != nil {
		return err
	}

	if l.config.Role == RoleCache {
		l.removeCacheEntry(path)
	}

	return nil
}

func (l *LocalFS) GetFeatures() FileSystemFeatures {
	return l.config.Features
}

func (l *LocalFS) GetRole() FileSystemRole {
	return l.config.Role
}

func (l *LocalFS) GetUsage() (int64, error) {
	var size int64
	err := filepath.Walk(l.root, func(_ string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			size += info.Size()
		}
		return nil
	})
	return size, err
}

// Cache management methods
func (l *LocalFS) updateCacheEntry(path string, size int64) {
	l.mutex.Lock()
	defer l.mutex.Unlock()

	// Remove existing entry if present
	for i, entry := range l.cacheList {
		if entry.Path == path {
			l.cacheList = append(l.cacheList[:i], l.cacheList[i+1:]...)
			break
		}
	}

	// Add new entry
	l.cacheList = append(l.cacheList, CacheEntry{
		Path:     path,
		Size:     size,
		LastUsed: time.Now(),
	})
}

func (l *LocalFS) removeCacheEntry(path string) {
	l.mutex.Lock()
	defer l.mutex.Unlock()

	for i, entry := range l.cacheList {
		if entry.Path == path {
			l.cacheList = append(l.cacheList[:i], l.cacheList[i+1:]...)
			break
		}
	}
}

func (l *LocalFS) ensureCacheSpace(needed int64) error {
	if l.config.Role != RoleCache {
		return nil
	}

	l.mutex.Lock()
	defer l.mutex.Unlock()

	// Calculate current usage
	var currentSize int64
	for _, entry := range l.cacheList {
		currentSize += entry.Size
	}

	// If we're over capacity, remove oldest entries until we have space
	for currentSize+needed > l.config.MaxSize && len(l.cacheList) > 0 {
		// Find oldest entry
		oldestIdx := 0
		for i, entry := range l.cacheList {
			if entry.LastUsed.Before(l.cacheList[oldestIdx].LastUsed) {
				oldestIdx = i
			}
		}

		// Remove the file
		oldestEntry := l.cacheList[oldestIdx]
		fullPath := filepath.Join(l.root, oldestEntry.Path)
		if err := os.Remove(fullPath); err != nil {
			return err
		}

		// Update tracking
		currentSize -= oldestEntry.Size
		l.cacheList = append(l.cacheList[:oldestIdx], l.cacheList[oldestIdx+1:]...)
	}

	return nil
}
