package main

import (
	"fmt"
	"os"
	"sync"
)

// ChainFS implements ServerFS and manages a chain of filesystems
type ChainFS struct {
	filesystems []ServerFS
	mutex       sync.RWMutex
}

// NewChainFS creates a new ChainFS with the given filesystems
func NewChainFS(filesystems []ServerFS) *ChainFS {
	return &ChainFS{
		filesystems: filesystems,
	}
}

// findFirstLockableFS returns the first filesystem that supports locking
func (c *ChainFS) findFirstLockableFS() (ServerFS, error) {
	for _, fs := range c.filesystems {
		if fs.GetFeatures().CanLock {
			return fs, nil
		}
	}
	return nil, fmt.Errorf("no filesystem in the chain supports locking")
}

// Lock implements file locking using the first filesystem that supports it
func (c *ChainFS) Lock(path string, lockType LockType, processID int) error {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	fs, err := c.findFirstLockableFS()
	if err != nil {
		return err
	}

	return fs.Lock(path, lockType, processID)
}

// Unlock removes a lock using the first filesystem that supports locking
func (c *ChainFS) Unlock(path string, processID int) error {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	fs, err := c.findFirstLockableFS()
	if err != nil {
		return err
	}

	return fs.Unlock(path, processID)
}

// IsLocked checks if a file is locked using the first filesystem that supports locking
func (c *ChainFS) IsLocked(path string) (bool, LockType, error) {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	fs, err := c.findFirstLockableFS()
	if err != nil {
		return false, 0, err
	}

	return fs.IsLocked(path)
}

// Info implements the chain of responsibility for getting file info
func (c *ChainFS) Info(path string) (FileInfo, error) {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	var lastErr error
	for _, fs := range c.filesystems {
		info, err := fs.Info(path)
		if err == nil {
			return info, nil
		}
		lastErr = err
	}
	return FileInfo{}, lastErr
}

// List implements the chain of responsibility for listing files
func (c *ChainFS) List(path string) ([]FileInfo, error) {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	var lastErr error
	for _, fs := range c.filesystems {
		files, err := fs.List(path)
		if err == nil {
			return files, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// Read implements the chain of responsibility for reading files
func (c *ChainFS) Read(path string) ([]byte, error) {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	// Check if file is locked
	if locked, lockType, err := c.IsLocked(path); err == nil && locked {
		if lockType == WriteLock || lockType == ExclusiveLock {
			return nil, fmt.Errorf("file is locked for writing")
		}
	}

	var lastErr error
	var content []byte

	// Try to read from each filesystem in order
	for i, fs := range c.filesystems {
		content, lastErr = fs.Read(path)
		if lastErr == nil {
			// File found, propagate it back through the chain
			c.propagateContent(path, content, i)
			return content, nil
		}
	}

	return nil, lastErr
}

// propagateContent writes the content to all filesystems before the found index
func (c *ChainFS) propagateContent(path string, content []byte, foundIndex int) {
	for i := foundIndex - 1; i >= 0; i-- {
		fs := c.filesystems[i]
		if fs.GetFeatures().CanUpdate {
			// Attempt to cache the content, ignore errors
			_ = fs.Write(path, content, 0644)
		}
	}
}

// Write implements the chain of responsibility for writing files
func (c *ChainFS) Write(path string, content []byte, mode os.FileMode) error {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	// Check if file is locked
	if locked, _, err := c.IsLocked(path); err == nil && locked {
		return fmt.Errorf("file is locked")
	}

	// Write to all filesystems that support updates
	var lastErr error
	for _, fs := range c.filesystems {
		if fs.GetFeatures().CanUpdate {
			if err := fs.Write(path, content, mode); err != nil {
				lastErr = err
			}
		}
	}
	return lastErr
}

// Delete implements the chain of responsibility for deleting files
func (c *ChainFS) Delete(path string) error {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	// Check if file is locked
	if locked, _, err := c.IsLocked(path); err == nil && locked {
		return fmt.Errorf("file is locked")
	}

	var lastErr error
	for _, fs := range c.filesystems {
		if fs.GetFeatures().CanDelete {
			if err := fs.Delete(path); err != nil {
				lastErr = err
			}
		}
	}
	return lastErr
}

// GetFeatures returns combined features of all filesystems
func (c *ChainFS) GetFeatures() FileSystemFeatures {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	features := FileSystemFeatures{}
	for _, fs := range c.filesystems {
		fsFeatures := fs.GetFeatures()
		features.CanUpdate = features.CanUpdate || fsFeatures.CanUpdate
		features.CanDelete = features.CanDelete || fsFeatures.CanDelete
		features.CanLock = features.CanLock || fsFeatures.CanLock
	}
	return features
}

// GetRole always returns "chain" as this is a chain of filesystems
func (c *ChainFS) GetRole() FileSystemRole {
	return "chain"
}

// GetUsage returns the total usage across all filesystems
func (c *ChainFS) GetUsage() (int64, error) {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	var total int64
	for _, fs := range c.filesystems {
		usage, err := fs.GetUsage()
		if err != nil {
			return 0, fmt.Errorf("error getting usage from filesystem: %v", err)
		}
		total += usage
	}
	return total, nil
}
