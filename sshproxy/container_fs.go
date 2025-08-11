package sshproxy

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ContainerManager defines the interface for managing containers
type ContainerManager interface {
	ExecuteInContainer(ctx context.Context, userID, containerID string, cmd []string, stdin io.Reader, stdout, stderr io.Writer) error
}

// UnixContainerFS implements ContainerFS using standard Unix commands
// This implementation works with any container that provides standard Unix utilities
type UnixContainerFS struct {
	manager     ContainerManager
	userID      string
	containerID string
	homeDir     string // Home directory in container (e.g., "/workspace")
}

// NewUnixContainerFS creates a new Unix-based container filesystem
func NewUnixContainerFS(manager ContainerManager, userID, containerID, homeDir string) *UnixContainerFS {
	return &UnixContainerFS{
		manager:     manager,
		userID:      userID,
		containerID: containerID,
		homeDir:     homeDir,
	}
}

// Stat returns file info for a path
func (fs *UnixContainerFS) Stat(ctx context.Context, path string) (os.FileInfo, error) {
	// Use stat command to get file info
	var stdout, stderr bytes.Buffer
	cmd := []string{"stat", "-c", "%n|%s|%Y|%f|%u|%g", path}
	
	err := fs.manager.ExecuteInContainer(ctx, fs.userID, fs.containerID, cmd, nil, &stdout, &stderr)
	if err != nil {
		if strings.Contains(stderr.String(), "No such file") || strings.Contains(stderr.String(), "cannot stat") {
			return nil, os.ErrNotExist
		}
		return nil, fmt.Errorf("stat failed: %v, stderr: %s", err, stderr.String())
	}
	
	return parseStatOutput(stdout.String(), filepath.Base(path))
}

// ReadDir lists directory contents
func (fs *UnixContainerFS) ReadDir(ctx context.Context, path string) ([]os.FileInfo, error) {
	var stdout, stderr bytes.Buffer
	// Use ls with detailed output for compatibility with BusyBox
	cmd := []string{"ls", "-la", path}
	
	err := fs.manager.ExecuteInContainer(ctx, fs.userID, fs.containerID, cmd, nil, &stdout, &stderr)
	if err != nil {
		return nil, fmt.Errorf("readdir failed: %v, stderr: %s", err, stderr.String())
	}
	
	return parseLsOutput(stdout.String())
}

// Open opens a file for reading
func (fs *UnixContainerFS) Open(ctx context.Context, path string) (io.ReadCloser, error) {
	return fs.OpenFile(ctx, path, os.O_RDONLY, 0)
}

// Create creates or truncates a file for writing
func (fs *UnixContainerFS) Create(ctx context.Context, path string) (io.WriteCloser, error) {
	return fs.OpenFile(ctx, path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
}

// OpenFile opens a file with specific flags and mode
func (fs *UnixContainerFS) OpenFile(ctx context.Context, path string, flags int, mode os.FileMode) (File, error) {
	return &unixFile{
		fs:    fs,
		path:  path,
		flags: flags,
		mode:  mode,
		ctx:   ctx,
	}, nil
}

// Mkdir creates a directory
func (fs *UnixContainerFS) Mkdir(ctx context.Context, path string, mode os.FileMode) error {
	var stderr bytes.Buffer
	cmd := []string{"mkdir", "-p", path}
	
	err := fs.manager.ExecuteInContainer(ctx, fs.userID, fs.containerID, cmd, nil, nil, &stderr)
	if err != nil {
		return fmt.Errorf("mkdir failed: %v, stderr: %s", err, stderr.String())
	}
	
	// Set permissions if not default
	if mode != 0755 {
		cmd = []string{"chmod", fmt.Sprintf("%o", mode), path}
		err = fs.manager.ExecuteInContainer(ctx, fs.userID, fs.containerID, cmd, nil, nil, &stderr)
		if err != nil {
			return fmt.Errorf("chmod failed: %v, stderr: %s", err, stderr.String())
		}
	}
	
	return nil
}

// Remove removes a file or empty directory
func (fs *UnixContainerFS) Remove(ctx context.Context, path string) error {
	var stderr bytes.Buffer
	cmd := []string{"rm", "-f", path}
	
	err := fs.manager.ExecuteInContainer(ctx, fs.userID, fs.containerID, cmd, nil, nil, &stderr)
	if err != nil {
		// Try rmdir if rm fails (might be a directory)
		cmd = []string{"rmdir", path}
		err = fs.manager.ExecuteInContainer(ctx, fs.userID, fs.containerID, cmd, nil, nil, &stderr)
		if err != nil {
			return fmt.Errorf("remove failed: %v, stderr: %s", err, stderr.String())
		}
	}
	
	return nil
}

// RemoveAll removes a path and any children
func (fs *UnixContainerFS) RemoveAll(ctx context.Context, path string) error {
	var stderr bytes.Buffer
	cmd := []string{"rm", "-rf", path}
	
	err := fs.manager.ExecuteInContainer(ctx, fs.userID, fs.containerID, cmd, nil, nil, &stderr)
	if err != nil {
		return fmt.Errorf("removeall failed: %v, stderr: %s", err, stderr.String())
	}
	
	return nil
}

// Rename renames/moves a file or directory
func (fs *UnixContainerFS) Rename(ctx context.Context, oldPath, newPath string) error {
	var stderr bytes.Buffer
	cmd := []string{"mv", oldPath, newPath}
	
	err := fs.manager.ExecuteInContainer(ctx, fs.userID, fs.containerID, cmd, nil, nil, &stderr)
	if err != nil {
		return fmt.Errorf("rename failed: %v, stderr: %s", err, stderr.String())
	}
	
	return nil
}

// Symlink creates a symbolic link
func (fs *UnixContainerFS) Symlink(ctx context.Context, target, link string) error {
	var stderr bytes.Buffer
	// ln -s target link (creates 'link' pointing to 'target')
	cmd := []string{"ln", "-s", target, link}
	
	err := fs.manager.ExecuteInContainer(ctx, fs.userID, fs.containerID, cmd, nil, nil, &stderr)
	if err != nil {
		return fmt.Errorf("symlink failed: %v, stderr: %s", err, stderr.String())
	}
	
	return nil
}

// Readlink reads a symbolic link
func (fs *UnixContainerFS) Readlink(ctx context.Context, path string) (string, error) {
	var stdout, stderr bytes.Buffer
	cmd := []string{"readlink", path}
	
	err := fs.manager.ExecuteInContainer(ctx, fs.userID, fs.containerID, cmd, nil, &stdout, &stderr)
	if err != nil {
		return "", fmt.Errorf("readlink failed: %v, stderr: %s", err, stderr.String())
	}
	
	return strings.TrimSpace(stdout.String()), nil
}

// Chmod changes file mode
func (fs *UnixContainerFS) Chmod(ctx context.Context, path string, mode os.FileMode) error {
	var stderr bytes.Buffer
	cmd := []string{"chmod", fmt.Sprintf("%o", mode), path}
	
	err := fs.manager.ExecuteInContainer(ctx, fs.userID, fs.containerID, cmd, nil, nil, &stderr)
	if err != nil {
		return fmt.Errorf("chmod failed: %v, stderr: %s", err, stderr.String())
	}
	
	return nil
}

// Chown changes file ownership
func (fs *UnixContainerFS) Chown(ctx context.Context, path string, uid, gid int) error {
	var stderr bytes.Buffer
	cmd := []string{"chown", fmt.Sprintf("%d:%d", uid, gid), path}
	
	err := fs.manager.ExecuteInContainer(ctx, fs.userID, fs.containerID, cmd, nil, nil, &stderr)
	// Ignore errors as containers may not support chown
	_ = err
	return nil
}

// Chtimes changes file access and modification times
func (fs *UnixContainerFS) Chtimes(ctx context.Context, path string, atime, mtime int64) error {
	var stderr bytes.Buffer
	// Use touch command with specific times
	atimeStr := time.Unix(atime, 0).Format("200601021504.05")
	mtimeStr := time.Unix(mtime, 0).Format("200601021504.05")
	
	// Set access time
	cmd := []string{"touch", "-a", "-t", atimeStr, path}
	err := fs.manager.ExecuteInContainer(ctx, fs.userID, fs.containerID, cmd, nil, nil, &stderr)
	if err != nil {
		return fmt.Errorf("setting atime failed: %v, stderr: %s", err, stderr.String())
	}
	
	// Set modification time
	cmd = []string{"touch", "-m", "-t", mtimeStr, path}
	err = fs.manager.ExecuteInContainer(ctx, fs.userID, fs.containerID, cmd, nil, nil, &stderr)
	if err != nil {
		return fmt.Errorf("setting mtime failed: %v, stderr: %s", err, stderr.String())
	}
	
	return nil
}

// unixFile implements the File interface for Unix-based containers
type unixFile struct {
	fs     *UnixContainerFS
	path   string
	flags  int
	mode   os.FileMode
	ctx    context.Context
	
	// For write operations, we buffer data
	writeBuffer []byte
	mu          sync.Mutex
	closed      bool
}

// Read reads from the file
func (f *unixFile) Read(p []byte) (int, error) {
	return f.ReadAt(p, 0)
}

// ReadAt reads from the file at a specific offset
func (f *unixFile) ReadAt(p []byte, offset int64) (int, error) {
	if f.closed {
		return 0, fmt.Errorf("file is closed")
	}
	
	// Use dd to read specific bytes from file
	var stdout, stderr bytes.Buffer
	cmd := []string{
		"dd",
		fmt.Sprintf("if=%s", f.path),
		"bs=1",
		fmt.Sprintf("skip=%d", offset),
		fmt.Sprintf("count=%d", len(p)),
		"status=none",
	}
	
	err := f.fs.manager.ExecuteInContainer(f.ctx, f.fs.userID, f.fs.containerID, cmd, nil, &stdout, &stderr)
	if err != nil {
		if strings.Contains(stderr.String(), "No such file") {
			return 0, os.ErrNotExist
		}
		return 0, fmt.Errorf("read failed: %v, stderr: %s", err, stderr.String())
	}
	
	n := copy(p, stdout.Bytes())
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

// Write writes to the file
func (f *unixFile) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	
	if f.closed {
		return 0, fmt.Errorf("file is closed")
	}
	
	f.writeBuffer = append(f.writeBuffer, p...)
	return len(p), nil
}

// WriteAt writes to the file at a specific offset
func (f *unixFile) WriteAt(p []byte, offset int64) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	
	if f.closed {
		return 0, fmt.Errorf("file is closed")
	}
	
	// Extend buffer if necessary
	endPos := int(offset) + len(p)
	if endPos > len(f.writeBuffer) {
		newBuffer := make([]byte, endPos)
		copy(newBuffer, f.writeBuffer)
		f.writeBuffer = newBuffer
	}
	
	// Write data at offset
	copy(f.writeBuffer[offset:], p)
	return len(p), nil
}

// Seek changes the file position
func (f *unixFile) Seek(offset int64, whence int) (int64, error) {
	// For simplicity, we don't maintain a position
	// Each read/write operation specifies its own offset
	return 0, fmt.Errorf("seek not implemented")
}

// Close closes the file and flushes any buffered writes
func (f *unixFile) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	
	if f.closed {
		return nil
	}
	
	// If we have buffered writes, flush them
	if len(f.writeBuffer) > 0 {
		// Encode data as base64 for safe transfer
		encoded := base64.StdEncoding.EncodeToString(f.writeBuffer)
		
		// Write to file using base64 decoding
		var stderr bytes.Buffer
		cmd := []string{"sh", "-c", fmt.Sprintf("echo '%s' | base64 -d > '%s'", encoded, f.path)}
		
		err := f.fs.manager.ExecuteInContainer(f.ctx, f.fs.userID, f.fs.containerID, cmd, nil, nil, &stderr)
		if err != nil {
			return fmt.Errorf("write failed: %v, stderr: %s", err, stderr.String())
		}
		
		// Set file mode if creating
		if f.flags&os.O_CREATE != 0 && f.mode != 0 {
			cmd = []string{"chmod", fmt.Sprintf("%o", f.mode), f.path}
			f.fs.manager.ExecuteInContainer(f.ctx, f.fs.userID, f.fs.containerID, cmd, nil, nil, nil)
		}
	}
	
	f.closed = true
	return nil
}

// Stat returns file info
func (f *unixFile) Stat() (os.FileInfo, error) {
	return f.fs.Stat(f.ctx, f.path)
}

// Sync commits the file contents to stable storage
func (f *unixFile) Sync() error {
	// In container context, Close handles the write
	return nil
}

// Truncate changes the size of the file
func (f *unixFile) Truncate(size int64) error {
	var stderr bytes.Buffer
	cmd := []string{"truncate", "-s", strconv.FormatInt(size, 10), f.path}
	
	err := f.fs.manager.ExecuteInContainer(f.ctx, f.fs.userID, f.fs.containerID, cmd, nil, nil, &stderr)
	if err != nil {
		return fmt.Errorf("truncate failed: %v, stderr: %s", err, stderr.String())
	}
	
	return nil
}

// Helper functions for parsing command output

func parseStatOutput(output, name string) (os.FileInfo, error) {
	parts := strings.Split(strings.TrimSpace(output), "|")
	if len(parts) < 4 {
		return nil, fmt.Errorf("invalid stat output")
	}
	
	size, _ := strconv.ParseInt(parts[1], 10, 64)
	mtime, _ := strconv.ParseInt(parts[2], 10, 64)
	modeHex, _ := strconv.ParseUint(parts[3], 16, 32)
	
	return &fileInfo{
		name:  name,
		size:  size,
		mode:  os.FileMode(modeHex),
		mtime: time.Unix(mtime, 0),
	}, nil
}

func parseLsOutput(output string) ([]os.FileInfo, error) {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	var entries []os.FileInfo
	
	for _, line := range lines {
		if line == "" || strings.HasPrefix(line, "total") {
			continue
		}
		
		// Parse ls -la output format:
		// -rw-r--r-- 1 user group size date time name
		fields := strings.Fields(line)
		if len(fields) < 9 {
			continue
		}
		
		// Skip . and .. entries
		name := strings.Join(fields[8:], " ")
		if name == "." || name == ".." {
			continue
		}
		
		// Parse permissions
		perms := fields[0]
		mode := parseFileMode(perms)
		
		// Parse size
		size, _ := strconv.ParseInt(fields[4], 10, 64)
		
		// For now, use current time as mtime (ls output varies)
		mtime := time.Now()
		
		entries = append(entries, &fileInfo{
			name:  name,
			size:  size,
			mode:  mode,
			mtime: mtime,
		})
	}
	
	return entries, nil
}

func parseFileMode(perms string) os.FileMode {
	if len(perms) < 10 {
		return 0755
	}
	
	var mode os.FileMode
	
	// File type
	switch perms[0] {
	case 'd':
		mode |= os.ModeDir
	case 'l':
		mode |= os.ModeSymlink
	case 'p':
		mode |= os.ModeNamedPipe
	case 's':
		mode |= os.ModeSocket
	case 'c':
		mode |= os.ModeCharDevice
	case 'b':
		mode |= os.ModeDevice
	}
	
	// Parse rwx permissions more simply
	var perm os.FileMode
	
	// Owner permissions (positions 1-3)
	if perms[1] == 'r' { perm |= 0400 }
	if perms[2] == 'w' { perm |= 0200 }
	if perms[3] == 'x' || perms[3] == 's' { perm |= 0100 }
	
	// Group permissions (positions 4-6)
	if perms[4] == 'r' { perm |= 0040 }
	if perms[5] == 'w' { perm |= 0020 }
	if perms[6] == 'x' || perms[6] == 's' { perm |= 0010 }
	
	// Other permissions (positions 7-9)
	if perms[7] == 'r' { perm |= 0004 }
	if perms[8] == 'w' { perm |= 0002 }
	if perms[9] == 'x' || perms[9] == 't' { perm |= 0001 }
	
	return mode | perm
}

// fileInfo implements os.FileInfo
type fileInfo struct {
	name  string
	size  int64
	mode  os.FileMode
	mtime time.Time
}

func (fi *fileInfo) Name() string       { return fi.name }
func (fi *fileInfo) Size() int64        { return fi.size }
func (fi *fileInfo) Mode() os.FileMode  { return fi.mode }
func (fi *fileInfo) ModTime() time.Time { return fi.mtime }
func (fi *fileInfo) IsDir() bool        { return fi.mode.IsDir() }
func (fi *fileInfo) Sys() interface{}   { return nil }