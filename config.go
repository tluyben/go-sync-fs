package main

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type FSConfig struct {
	Type      string `yaml:"type"`       // "local", "s3", etc
	Role      string `yaml:"role"`       // "main", "cache"
	Path      string `yaml:"path"`       // Local path or bucket path
	MaxSize   int64  `yaml:"max_size"`   // For cache filesystems
	CanUpdate bool   `yaml:"can_update"` // Whether writes are allowed
	CanDelete bool   `yaml:"can_delete"` // Whether deletes are allowed
	CanLock   bool   `yaml:"can_lock"`   // Whether file locking is supported
}

type Config struct {
	Mount       string     `yaml:"mount"`       // FUSE mount point
	ServerAddr  string     `yaml:"server_addr"` // Server address (host:port)
	FileSystems []FSConfig `yaml:"filesystems"` // List of filesystems in order
	HasLocking  bool       `yaml:"-"`           // Computed field indicating if chain supports locking
}

func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("error reading config file: %v", err)
	}

	var config Config
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("error parsing config file: %v", err)
	}

	// Validate config
	if config.Mount == "" {
		return nil, fmt.Errorf("mount point is required")
	}
	if len(config.FileSystems) == 0 {
		return nil, fmt.Errorf("at least one filesystem is required")
	}
	if config.ServerAddr == "" {
		config.ServerAddr = ":8080" // Default server address
	}

	// Validate that the first filesystem supports locking if any filesystem does
	for i, fs := range config.FileSystems {
		if fs.CanLock {
			config.HasLocking = true
			if i > 0 {
				return nil, fmt.Errorf("only the first filesystem in the chain can support locking")
			}
			break
		}
	}

	return &config, nil
}

func createFileSystems(config *Config) ([]ServerFS, error) {
	var filesystems []ServerFS

	for _, fsConfig := range config.FileSystems {
		features := FileSystemFeatures{
			CanUpdate: fsConfig.CanUpdate,
			CanDelete: fsConfig.CanDelete,
			CanLock:   fsConfig.CanLock,
		}

		fsRole := FileSystemRole(fsConfig.Role)
		if fsRole != RoleMain && fsRole != RoleCache {
			return nil, fmt.Errorf("invalid role for filesystem: %s", fsConfig.Role)
		}

		switch fsConfig.Type {
		case "local":
			fs, err := NewLocalFS(FileSystemConfig{
				Role:     fsRole,
				MaxSize:  fsConfig.MaxSize,
				Features: features,
				RootPath: fsConfig.Path,
			})
			if err != nil {
				return nil, fmt.Errorf("error creating local filesystem: %v", err)
			}
			filesystems = append(filesystems, fs)
		// Add other filesystem types here (S3, FTP, etc.)
		default:
			return nil, fmt.Errorf("unsupported filesystem type: %s", fsConfig.Type)
		}
	}

	return filesystems, nil
}
