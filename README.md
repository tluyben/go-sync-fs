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
- 🔒 File locking with multiple lock types

## 🏗️ Architecture

The project consists of three main components:

1. **File Server** 🖥️
   - Handles file system operations via HTTP endpoints
   - Supports basic file operations (read/write/list/info)
   - Configurable roles (main/cache)
   - Built-in cache size management
   - Process-specific file locking

2. **FUSE Client** 📂
   - Mounts remote file system locally
   - Transparent file access
   - Real-time synchronization with server
   - Native file system integration
   - Automatic lock management based on file open modes

3. **Chain Filesystem** ⛓️
   - Manages multiple filesystems in a chain
   - Automatic content propagation through caches
   - Configurable through YAML
   - Extensible for different backend types (local, S3, etc.)
   - First-filesystem locking strategy

## 🔒 File Locking

The system implements a robust file locking mechanism with the following features:

1. **Lock Types**
   - ReadLock: Multiple readers allowed
   - WriteLock: Single writer, no readers
   - ExclusiveLock: No other access allowed

2. **Chain-of-Responsibility**
   - Only the first filesystem in the chain needs to support locking
   - Locking state is managed by the first filesystem
   - Other filesystems inherit the locking state

3. **FUSE Integration**
   - Automatic lock acquisition based on file open flags:
     * Read-only opens acquire read locks
     * Write-only opens acquire write locks
     * Read-write opens acquire exclusive locks
   - Automatic lock release on file close

4. **Process Safety**
   - Locks are tracked per process ID
   - Only the process that acquired a lock can release it
   - Prevents lock stealing between processes

## 🛠️ API Endpoints

- `/info` - Get file/directory information
- `/list` - List directory contents
- `/read` - Read file contents
- `/write` - Write file contents
- `/lock` - Acquire a file lock
- `/unlock` - Release a file lock

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
  # Fast local cache with locking support
  - type: local
    role: cache
    path: /tmp/fs-cache
    max_size: 1073741824  # 1GB
    can_update: true
    can_delete: true
    can_lock: true  # Enable file locking (must be first in chain)

  # Main storage
  - type: local
    role: main
    path: /home/user/data
    can_update: true
    can_delete: true
    can_lock: false  # Optional for non-first filesystems
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
- File locking mechanism with multiple lock types
- Process-specific lock tracking
- FUSE-integrated lock management

### 🚧 Planned/In Progress
- Additional backend types (S3, FTP, etc.)
- Enhanced error handling
- Better cache management
- Improved synchronization
- Security features

## 🤝 Contributing

Contributions are welcome! Feel free to submit issues and pull requests.

## ⚠️ Current Limitations

- Write operations need further testing
- No built-in security features yet
- Limited error recovery mechanisms
- Currently only local filesystem backend implemented

## 📝 License

[License Information Here]

---
⚡️ Built with Go and FUSE for high-performance file system operations
