package sshproxy

import (
	"context"
	"io"
	"os"
)

// ContainerFS defines the interface for container filesystem operations
// This abstracts away the underlying container implementation (Docker, etc.)
type ContainerFS interface {
	// Stat returns file info for a path (follows symlinks)
	Stat(ctx context.Context, path string) (os.FileInfo, error)

	// Lstat returns file info for a path (does not follow symlinks)
	Lstat(ctx context.Context, path string) (os.FileInfo, error)

	// ReadDir lists directory contents
	ReadDir(ctx context.Context, path string) ([]os.FileInfo, error)

	// Open opens a file for reading
	Open(ctx context.Context, path string) (io.ReadCloser, error)

	// Create creates or truncates a file for writing
	Create(ctx context.Context, path string) (io.WriteCloser, error)

	// OpenFile opens a file with specific flags and mode
	OpenFile(ctx context.Context, path string, flags int, mode os.FileMode) (File, error)

	// Mkdir creates a directory
	Mkdir(ctx context.Context, path string, mode os.FileMode) error

	// Remove removes a file or empty directory
	Remove(ctx context.Context, path string) error

	// RemoveAll removes a path and any children
	RemoveAll(ctx context.Context, path string) error

	// Rename renames/moves a file or directory
	Rename(ctx context.Context, oldPath, newPath string) error

	// Symlink creates a symbolic link
	Symlink(ctx context.Context, target, link string) error

	// Readlink reads a symbolic link
	Readlink(ctx context.Context, path string) (string, error)

	// Chmod changes file mode
	Chmod(ctx context.Context, path string, mode os.FileMode) error

	// Chown changes file ownership (may be no-op in containers)
	Chown(ctx context.Context, path string, uid, gid int) error

	// Chtimes changes file access and modification times
	Chtimes(ctx context.Context, path string, atime, mtime int64) error
}

// File represents an open file in the container
type File interface {
	io.ReadWriteCloser
	io.ReaderAt
	io.WriterAt
	io.Seeker

	// Stat returns file info
	Stat() (os.FileInfo, error)

	// Sync commits the file contents to stable storage
	Sync() error

	// Truncate changes the size of the file
	Truncate(size int64) error
}

// ContainerExecutor defines the interface for executing commands in containers
type ContainerExecutor interface {
	// Execute runs a command in the container
	Execute(ctx context.Context, cmd []string, stdin io.Reader, stdout, stderr io.Writer) error

	// ExecuteWithPTY runs a command with a pseudo-terminal
	ExecuteWithPTY(ctx context.Context, cmd []string, stdin io.Reader, stdout io.Writer, term Terminal) error
}

// Terminal represents terminal settings
type Terminal struct {
	Term   string
	Width  uint32
	Height uint32
	Modes  map[uint8]uint32
}
