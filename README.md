# Go Sync FS 🚀

A powerful FUSE-based file system synchronization tool written in Go that enables seamless file system operations across networks.

## 🌟 Features

- 📁 FUSE-based file system mounting
- 🔄 Real-time file synchronization
- 🌐 HTTP-based file server
- 💾 Configurable caching system
- 🔐 Role-based file system (main/cache)
- 📊 Support for both files and directories
- 🔗 Chain of filesystems with automatic propagation
- 📝 YAML-based configuration

## 🏗️ Architecture

The project consists of three main components:

1. **File Server** 🖥️
   - Handles file system operations via HTTP endpoints
   - Supports basic file operations (read/write/list/info)
   - Configurable roles (main/cache)
   - Built-in cache size management

2. **FUSE Client** 📂
   - Mounts remote file system locally
   - Transparent file access
   - Real-time synchronization with server
   - Native file system integration

3. **Chain Filesystem** ⛓️
   - Manages multiple filesystems in a chain
   - Automatic content propagation through caches
   - Configurable through YAML
   - Extensible for different backend types (local, S3, etc.)

## 🛠️ API Endpoints

- `/info` - Get file/directory information
- `/list` - List directory contents
- `/read` - Read file contents
- `/write` - Write file contents

## 🚀 Getting Started

### Prerequisites

- Go 1.x
- FUSE installed on your system
- Linux/Unix-based operating system

### Installation

```bash
# Clone the repository
git clone [repository-url]

# Build the project
make build
```

### Usage

You can start the service either using command-line arguments (legacy mode) or using a YAML configuration file (recommended).

#### Using YAML Configuration (Recommended)

1. Create a configuration file (e.g., `config.yaml`):
```yaml
mount: /mnt/synced
server_addr: :8080

filesystems:
  # Fast local cache
  - type: local
    role: cache
    path: /tmp/fs-cache
    max_size: 1073741824  # 1GB
    can_update: true
    can_delete: true

  # Main storage
  - type: local
    role: main
    path: /home/user/data
    can_update: true
    can_delete: true
```

2. Start the service:
```bash
./go-sync-fs -config config.yaml
```

#### Using Command Line (Legacy)

```bash
# Start the server with a master directory
./go-sync-fs -master /path/to/master/dir -mount /path/to/mountpoint -server :8080 -role main

# Start a cache node
./go-sync-fs -master /path/to/cache/dir -mount /path/to/mountpoint -server :8081 -role cache -cache-size 1073741824
```

### Command Line Options

- `-config`: Path to YAML configuration file
- `-master`: Master directory to serve files from (legacy)
- `-server`: Server address (host:port) (legacy)
- `-mount`: Directory to mount FUSE filesystem (legacy)
- `-role`: Filesystem role (main or cache) (legacy)
- `-cache-size`: Max cache size in bytes (default 1GB) (legacy)

## 🔧 Current Implementation Status

### ✅ Implemented Features
- Basic FUSE operations (read, lookup, readdir)
- HTTP server endpoints
- File system roles (main/cache)
- Directory listing
- File content reading
- Basic write support
- Chain of filesystems
- YAML configuration
- Automatic cache propagation

### 🚧 Planned/In Progress
- Additional backend types (S3, FTP, etc.)
- File locking mechanism
- Enhanced error handling
- Better cache management
- Improved synchronization
- Security features

## 🤝 Contributing

Contributions are welcome! Feel free to submit issues and pull requests.

## ⚠️ Current Limitations

- Write operations need further testing
- No built-in security features yet
- File locking not implemented
- Limited error recovery mechanisms
- Currently only local filesystem backend implemented

## 📝 License

[License Information Here]

---
⚡️ Built with Go and FUSE for high-performance file system operations
