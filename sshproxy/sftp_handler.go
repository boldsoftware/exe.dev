package sshproxy

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pkg/sftp"
)

// SFTPHandler implements SFTP server operations using ContainerFS
type SFTPHandler struct {
	fs      ContainerFS
	homeDir string // Home directory in the container (e.g., "/workspace")
	ctx     context.Context
}

// NewSFTPHandler creates a new SFTP handler
func NewSFTPHandler(ctx context.Context, fs ContainerFS, homeDir string) *SFTPHandler {
	return &SFTPHandler{
		fs:      fs,
		homeDir: homeDir,
		ctx:     ctx,
	}
}

// resolvePath converts SFTP paths to absolute container paths
func (h *SFTPHandler) resolvePath(sftpPath string) string {
	// Handle empty path as home directory
	if sftpPath == "" || sftpPath == "." {
		return h.homeDir
	}
	
	// Handle ~ for home directory
	if sftpPath == "~" || sftpPath == "/~" {
		return h.homeDir
	}
	
	// CRITICAL: When user types "scp file host:~", OpenSSH sends "/" as the path
	// This should map to the home directory, not the root
	if sftpPath == "/" {
		return h.homeDir
	}
	
	// Handle tilde paths - some SFTP clients send /~/path instead of ~/path
	if strings.HasPrefix(sftpPath, "/~/") {
		return filepath.Join(h.homeDir, sftpPath[3:])
	}
	if strings.HasPrefix(sftpPath, "~/") {
		return filepath.Join(h.homeDir, sftpPath[2:])
	}
	
	// Handle absolute paths
	if filepath.IsAbs(sftpPath) {
		// If path is already absolute, clean it
		return filepath.Clean(sftpPath)
	}
	
	// Relative paths are relative to home directory
	return filepath.Join(h.homeDir, sftpPath)
}

// Fileread implements sftp.FileReader
func (h *SFTPHandler) Fileread(req *sftp.Request) (io.ReaderAt, error) {
	path := h.resolvePath(req.Filepath)
	
	// Open the file for reading
	file, err := h.fs.OpenFile(h.ctx, path, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	
	return &readerAtWrapper{file: file}, nil
}

// Filewrite implements sftp.FileWriter
func (h *SFTPHandler) Filewrite(req *sftp.Request) (io.WriterAt, error) {
	originalPath := req.Filepath
	filePath := h.resolvePath(originalPath)
	
	
	
	// Check if the path points to an existing directory OR
	// if it's a special path that should be treated as a directory
	// When SCP uploads to a directory (e.g., "scp file.txt host:~"),
	// the standard behavior is to extract the filename from the source
	// and create the file in that directory.
	
	// First check if it exists and is a directory
	info, err := h.fs.Stat(h.ctx, filePath)
	if err == nil && info.IsDir() {
		// The path is a directory. The SFTP protocol doesn't provide
		// the source filename, so we need to handle this specially.
		// 
		// For paths like "~" or ".", we'll accept the write but store
		// the data in a special wrapper that will figure out the actual
		// filename when more information is available.
		
		// Return a special wrapper that handles directory uploads
		return &directoryUploadWrapper{
			fs:       h.fs,
			ctx:      h.ctx,
			dirPath:  filePath,
			origPath: originalPath,
		}, nil
	}
	
	
	// Determine flags based on request flags
	flags := os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	if req.Pflags().Append {
		flags = os.O_CREATE | os.O_WRONLY | os.O_APPEND
	}
	if req.Pflags().Excl {
		flags |= os.O_EXCL
	}
	
	// Open/create the file
	file, err := h.fs.OpenFile(h.ctx, filePath, flags, 0644)
	if err != nil {
		return nil, err
	}
	
	return &writerAtWrapper{file: file, path: filePath}, nil
}

// Filecmd implements sftp.FileCmder
func (h *SFTPHandler) Filecmd(req *sftp.Request) error {
	switch req.Method {
	case "Setstat":
		return h.setstat(req.Filepath, req.Attributes())
	case "Rename":
		return h.rename(req.Filepath, req.Target)
	case "Remove":
		return h.remove(req.Filepath)
	case "Mkdir":
		return h.mkdir(req.Filepath, req.Attributes())
	case "Rmdir":
		return h.rmdir(req.Filepath)
	case "Symlink":
		// Note: SFTP protocol puts the link path in Target and target in Filepath
		// This is opposite of what you might expect!
		return h.symlink(req.Filepath, req.Target)
	case "Link":
		// Hard links not supported in containers
		return fmt.Errorf("hard links not supported")
	default:
		return fmt.Errorf("unsupported command: %s", req.Method)
	}
}

// Filelist implements sftp.FileLister
func (h *SFTPHandler) Filelist(req *sftp.Request) (sftp.ListerAt, error) {
	switch req.Method {
	case "List":
		return h.list(req.Filepath)
	case "Stat":
		return h.stat(req.Filepath)
	case "Lstat":
		return h.lstat(req.Filepath)
	case "Readlink":
		return h.readlink(req.Filepath)
	default:
		return nil, fmt.Errorf("unsupported list command: %s", req.Method)
	}
}

// list returns directory contents
func (h *SFTPHandler) list(sftpPath string) (sftp.ListerAt, error) {
	path := h.resolvePath(sftpPath)
	
	entries, err := h.fs.ReadDir(h.ctx, path)
	if err != nil {
		return nil, err
	}
	
	return &listerAt{entries: entries}, nil
}

// stat returns file info for a single file
func (h *SFTPHandler) stat(sftpPath string) (sftp.ListerAt, error) {
	path := h.resolvePath(sftpPath)
	
	// First try lstat to check if it's a symlink
	// The SFTP client library seems to send "Stat" for both stat and lstat
	info, err := h.fs.Lstat(h.ctx, path)
	if err != nil {
		// If lstat fails, fall back to regular stat
		info, err = h.fs.Stat(h.ctx, path)
		if err != nil {
			return nil, err
		}
	}
	
	return &listerAt{entries: []os.FileInfo{info}}, nil
}

// lstat returns file info without following symlinks
func (h *SFTPHandler) lstat(sftpPath string) (sftp.ListerAt, error) {
	path := h.resolvePath(sftpPath)
	
	info, err := h.fs.Lstat(h.ctx, path)
	if err != nil {
		return nil, err
	}
	
	return &listerAt{entries: []os.FileInfo{info}}, nil
}

// readlink reads a symbolic link
func (h *SFTPHandler) readlink(sftpPath string) (sftp.ListerAt, error) {
	path := h.resolvePath(sftpPath)
	
	target, err := h.fs.Readlink(h.ctx, path)
	if err != nil {
		return nil, err
	}
	
	// Return a fake FileInfo with the link target as the name
	info := &linkInfo{name: target}
	return &listerAt{entries: []os.FileInfo{info}}, nil
}

// setstat sets file attributes
func (h *SFTPHandler) setstat(sftpPath string, attrs *sftp.FileStat) error {
	path := h.resolvePath(sftpPath)
	
	// Set file mode if specified
	if attrs.Mode != 0 {
		if err := h.fs.Chmod(h.ctx, path, os.FileMode(attrs.Mode)); err != nil {
			return err
		}
	}
	
	// Set times if specified
	if attrs.Atime != 0 || attrs.Mtime != 0 {
		atime := int64(attrs.Atime)
		mtime := int64(attrs.Mtime)
		if err := h.fs.Chtimes(h.ctx, path, atime, mtime); err != nil {
			return err
		}
	}
	
	// Set ownership if specified (may be no-op)
	if attrs.UID != 0 || attrs.GID != 0 {
		if err := h.fs.Chown(h.ctx, path, int(attrs.UID), int(attrs.GID)); err != nil {
			// Ignore errors for chown as it may not be supported
			_ = err
		}
	}
	
	return nil
}

// rename moves/renames a file
func (h *SFTPHandler) rename(oldSftpPath, newSftpPath string) error {
	oldPath := h.resolvePath(oldSftpPath)
	newPath := h.resolvePath(newSftpPath)
	return h.fs.Rename(h.ctx, oldPath, newPath)
}

// remove deletes a file
func (h *SFTPHandler) remove(sftpPath string) error {
	path := h.resolvePath(sftpPath)
	return h.fs.Remove(h.ctx, path)
}

// mkdir creates a directory
func (h *SFTPHandler) mkdir(sftpPath string, attrs *sftp.FileStat) error {
	path := h.resolvePath(sftpPath)
	mode := os.FileMode(0755)
	if attrs.Mode != 0 {
		mode = os.FileMode(attrs.Mode)
	}
	return h.fs.Mkdir(h.ctx, path, mode)
}

// rmdir removes an empty directory
func (h *SFTPHandler) rmdir(sftpPath string) error {
	path := h.resolvePath(sftpPath)
	return h.fs.Remove(h.ctx, path)
}

// symlink creates a symbolic link
func (h *SFTPHandler) symlink(targetPath, linkPath string) error {
	// For symlinks, the target is stored as-is (can be relative)
	// Only resolve the link path to determine where to create it
	link := h.resolvePath(linkPath)
	// Target stays as provided (relative or absolute)
	return h.fs.Symlink(h.ctx, targetPath, link)
}

// Wrapper types for io interfaces

// directoryUploadWrapper handles uploads to directory paths
// It buffers the data and creates a file with an appropriate name
type directoryUploadWrapper struct {
	fs       ContainerFS
	ctx      context.Context
	dirPath  string
	origPath string
	buffer   []byte
	closed   bool
}

func (d *directoryUploadWrapper) WriteAt(p []byte, off int64) (int, error) {
	if d.closed {
		return 0, fmt.Errorf("file is closed")
	}
	
	// Extend buffer if necessary
	endPos := int(off) + len(p)
	if endPos > len(d.buffer) {
		newBuffer := make([]byte, endPos)
		copy(newBuffer, d.buffer)
		d.buffer = newBuffer
	}
	
	// Write data at offset
	copy(d.buffer[off:], p)
	return len(p), nil
}

func (d *directoryUploadWrapper) Close() error {
	if d.closed {
		return nil
	}
	d.closed = true
	
	// When closing, we need to actually create the file
	// Since we don't have the original filename, we'll use a default name
	// based on the original path
	
	var filename string
	if d.origPath == "~" || d.origPath == "/~" || d.origPath == "." {
		// For these special paths, generate a unique filename
		// In a proper implementation, SCP should send the full path with filename
		// This is a workaround for buggy SCP clients
		timestamp := time.Now().Format("20060102-150405")
		filename = fmt.Sprintf("scp-upload-%s", timestamp)
	} else {
		// Use the last component of the original path if available
		filename = filepath.Base(d.origPath)
		if filename == "/" || filename == "." {
			timestamp := time.Now().Format("20060102-150405")
			filename = fmt.Sprintf("scp-upload-%s", timestamp)
		}
	}
	
	// Create the actual file in the directory
	actualPath := filepath.Join(d.dirPath, filename)
	file, err := d.fs.OpenFile(d.ctx, actualPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("failed to create file %q: %v", actualPath, err)
	}
	
	// Write the buffered data
	if len(d.buffer) > 0 {
		_, err = file.Write(d.buffer)
		if err != nil {
			file.Close()
			return fmt.Errorf("failed to write data: %v", err)
		}
	}
	
	return file.Close()
}

type readerAtWrapper struct {
	file File
}

func (r *readerAtWrapper) ReadAt(p []byte, off int64) (int, error) {
	return r.file.ReadAt(p, off)
}

type writerAtWrapper struct {
	file File
	path string
}

func (w *writerAtWrapper) WriteAt(p []byte, off int64) (int, error) {
	return w.file.WriteAt(p, off)
}

// Close implements io.Closer for atomic file operations
func (w *writerAtWrapper) Close() error {
	return w.file.Close()
}

type listerAt struct {
	entries []os.FileInfo
	offset  int64
}

func (l *listerAt) ListAt(entries []os.FileInfo, offset int64) (int, error) {
	if offset >= int64(len(l.entries)) {
		return 0, io.EOF
	}
	
	n := copy(entries, l.entries[offset:])
	if n < len(entries) {
		return n, io.EOF
	}
	return n, nil
}

// linkInfo is a fake FileInfo for symbolic links
type linkInfo struct {
	name string
}

func (l *linkInfo) Name() string       { return l.name }
func (l *linkInfo) Size() int64        { return 0 }
func (l *linkInfo) Mode() os.FileMode  { return os.ModeSymlink }
func (l *linkInfo) ModTime() time.Time { return time.Now() }
func (l *linkInfo) IsDir() bool        { return false }
func (l *linkInfo) Sys() interface{}   { return nil }