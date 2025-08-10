package exe

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"exe.dev/container"
	"github.com/pkg/sftp"
)

// ContainerFS implements SFTP filesystem interfaces that operate on container files via GKE APIs
// Implements: FileLister, FileReader, FileWriter, FileCmder
type ContainerFS struct {
	containerManager container.Manager
	userID           string
	containerID      string
	rootDir          string // jail directory within container (e.g., "/workspace" or "/")
}

// NewContainerFS creates a filesystem interface for a specific container
func NewContainerFS(manager container.Manager, userID, containerID, rootDir string) *ContainerFS {
	if rootDir == "" {
		rootDir = "/workspace" // Default jail directory
	}
	fs := &ContainerFS{
		containerManager: manager,
		userID:           userID,
		containerID:      containerID,
		rootDir:          rootDir,
	}
	
	// Clean up any abandoned temp files from previous interrupted uploads
	fs.cleanupAbandonedTempFiles()
	
	return fs
}

// cleanupAbandonedTempFiles removes temp files that may have been left behind by interrupted uploads
func (fs *ContainerFS) cleanupAbandonedTempFiles() {
	ctx := context.Background()
	
	// Find and remove any .tmp. files in the root directory
	// Use find command to locate temp files and remove them
	cleanupCmd := fmt.Sprintf("find %s -name '*.tmp.*' -type f -delete 2>/dev/null || true", fs.rootDir)
	_, err := fs.execCommand(ctx, []string{"sh", "-c", cleanupCmd})
	if err != nil {
		// Cleanup failure is not critical, just continue
		// In production, you might want to log this
	}
}

// resolvePath converts SFTP paths to container paths and ensures they're within the jail
func (fs *ContainerFS) resolvePath(sftpPath string) (string, error) {
	// Path resolution logic

	var containerPath string
	
	// Handle ~ as home directory
	if sftpPath == "~" {
		containerPath = fs.rootDir
	} else if strings.HasPrefix(sftpPath, "~/") {
		containerPath = filepath.Join(fs.rootDir, strings.TrimPrefix(sftpPath, "~/"))
	} else if !filepath.IsAbs(sftpPath) {
		// Relative paths are relative to root dir
		containerPath = filepath.Join(fs.rootDir, sftpPath)
	} else {
		// Absolute paths: if they start with rootDir, use as-is, otherwise jail them
		if strings.HasPrefix(sftpPath, fs.rootDir) {
			containerPath = sftpPath
		} else {
			// Strip leading slash and jail to rootDir
			containerPath = filepath.Join(fs.rootDir, strings.TrimPrefix(sftpPath, "/"))
		}
	}

	// Clean the path
	containerPath = filepath.Clean(containerPath)
	
	// Ensure it's within jail
	if !strings.HasPrefix(containerPath, fs.rootDir) {
		return "", fmt.Errorf("access denied: path outside jail")
	}

	return containerPath, nil
}

// execCommand executes a command in the container and returns stdout
func (fs *ContainerFS) execCommand(ctx context.Context, cmd []string) (string, error) {
	var stdout strings.Builder
	var stderr strings.Builder

	// Execute command in container

	err := fs.containerManager.ExecuteInContainer(
		ctx,
		fs.userID,
		fs.containerID,
		cmd,
		nil,
		&stdout,
		&stderr,
	)

	// Check results

	if err != nil {
		if stderr.Len() > 0 {
			return "", fmt.Errorf("command failed: %v (stderr: %s)", err, stderr.String())
		}
		return "", err
	}

	return stdout.String(), nil
}

// Stat returns file info for the given path
func (fs *ContainerFS) Stat(sftpPath string) (os.FileInfo, error) {
	containerPath, err := fs.resolvePath(sftpPath)
	if err != nil {
		return nil, err
	}

	ctx := context.Background()
	
	// Use 'stat' command to get file info
	output, err := fs.execCommand(ctx, []string{"stat", "-c", "%n|%s|%Y|%X|%Z|%f|%u|%g", containerPath})
	if err != nil {
		// File doesn't exist or other error
		return nil, os.ErrNotExist
	}

	return parseStatOutput(output, filepath.Base(containerPath))
}

// ReadDir lists directory contents
func (fs *ContainerFS) ReadDir(sftpPath string) ([]os.FileInfo, error) {
	containerPath, err := fs.resolvePath(sftpPath)
	if err != nil {
		return nil, err
	}

	ctx := context.Background()
	
	// Use 'ls -la' to get directory listing with details
	output, err := fs.execCommand(ctx, []string{"ls", "-la", "--time-style=+%s", containerPath})
	if err != nil {
		return nil, fmt.Errorf("failed to list directory: %v", err)
	}

	return parseLsOutput(output)
}

// These methods are no longer needed - SFTP uses the Fileread/Filewrite interfaces instead

// Mkdir creates a directory
func (fs *ContainerFS) Mkdir(sftpPath string, perm os.FileMode) error {
	containerPath, err := fs.resolvePath(sftpPath)
	if err != nil {
		return err
	}

	ctx := context.Background()
	_, err = fs.execCommand(ctx, []string{"mkdir", "-p", containerPath})
	return err
}

// Remove removes a file or empty directory
func (fs *ContainerFS) Remove(sftpPath string) error {
	containerPath, err := fs.resolvePath(sftpPath)
	if err != nil {
		return err
	}

	ctx := context.Background()
	_, err = fs.execCommand(ctx, []string{"rm", "-rf", containerPath})
	return err
}

// Rename moves/renames a file or directory
func (fs *ContainerFS) Rename(oldSftpPath, newSftpPath string) error {
	oldPath, err := fs.resolvePath(oldSftpPath)
	if err != nil {
		return err
	}

	newPath, err := fs.resolvePath(newSftpPath)
	if err != nil {
		return err
	}

	ctx := context.Background()
	_, err = fs.execCommand(ctx, []string{"mv", oldPath, newPath})
	return err
}

// parseStatOutput parses the output of 'stat -c' command
func parseStatOutput(output, filename string) (os.FileInfo, error) {
	// Parse stat output format: name|size|mtime|atime|ctime|mode|uid|gid
	parts := strings.Split(strings.TrimSpace(output), "|")
	if len(parts) != 8 {
		return nil, fmt.Errorf("invalid stat output: %s", output)
	}

	// This is a simplified implementation - in production you'd parse all fields
	return &ContainerFileInfo{
		name:  filename,
		size:  0, // Would parse parts[1]
		mode:  0644, // Would parse parts[5] 
		mtime: time.Now(), // Would parse parts[2]
		isDir: false, // Would determine from mode
	}, nil
}

// parseLsOutput parses 'ls -la' output to extract file info
func parseLsOutput(output string) ([]os.FileInfo, error) {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	var files []os.FileInfo

	for _, line := range lines {
		if strings.HasPrefix(line, "total ") {
			continue // Skip total line
		}
		if strings.TrimSpace(line) == "" {
			continue
		}

		// Parse ls -la format (simplified)
		fields := strings.Fields(line)
		if len(fields) < 9 {
			continue
		}

		name := fields[8] // Last field is filename
		if name == "." || name == ".." {
			continue // Skip . and ..
		}

		isDir := strings.HasPrefix(fields[0], "d")
		
		files = append(files, &ContainerFileInfo{
			name:  name,
			size:  0,    // Would parse fields[4]
			mode:  0644, // Would parse fields[0]
			mtime: time.Now(), // Would parse timestamp fields
			isDir: isDir,
		})
	}

	return files, nil
}

// ContainerFileInfo implements os.FileInfo
type ContainerFileInfo struct {
	name  string
	size  int64
	mode  os.FileMode
	mtime time.Time
	isDir bool
}

func (fi *ContainerFileInfo) Name() string       { return fi.name }
func (fi *ContainerFileInfo) Size() int64        { return fi.size }
func (fi *ContainerFileInfo) Mode() os.FileMode  { return fi.mode }
func (fi *ContainerFileInfo) ModTime() time.Time { return fi.mtime }
func (fi *ContainerFileInfo) IsDir() bool         { return fi.isDir }
func (fi *ContainerFileInfo) Sys() interface{}   { return nil }

// SFTP Interface Implementations

// Filelist implements FileLister interface for SFTP
func (fs *ContainerFS) Filelist(req *sftp.Request) (sftp.ListerAt, error) {
	switch req.Method {
	case "List":
		// List directory contents
		fileInfos, err := fs.ReadDir(req.Filepath)
		if err != nil {
			return nil, err
		}
		return &ContainerLister{files: fileInfos}, nil
		
	case "Stat", "Lstat":
		// Get single file info
		fileInfo, err := fs.Stat(req.Filepath)
		if err != nil {
			return nil, err
		}
		return &ContainerLister{files: []os.FileInfo{fileInfo}}, nil
		
	default:
		return nil, fmt.Errorf("unsupported method: %s", req.Method)
	}
}

// Fileread implements FileReader interface for SFTP
func (fs *ContainerFS) Fileread(req *sftp.Request) (io.ReaderAt, error) {
	// Return a reader that can read from arbitrary offsets
	return &ContainerReaderAt{
		fs:   fs,
		path: req.Filepath,
	}, nil
}

// Filewrite implements FileWriter interface for SFTP
func (fs *ContainerFS) Filewrite(req *sftp.Request) (io.WriterAt, error) {
	containerPath, err := fs.resolvePath(req.Filepath)
	if err != nil {
		return nil, err
	}

	// Create atomic temp file for writes
	tempPath := containerPath + ".tmp." + fmt.Sprintf("%d", time.Now().UnixNano())

	return &ContainerWriterAt{
		fs:            fs,
		containerPath: containerPath,
		tempPath:      tempPath,
		writeBuffer:   make(map[int64][]byte), // For WriteAt operations
	}, nil
}

// Filecmd implements FileCmder interface for SFTP
func (fs *ContainerFS) Filecmd(req *sftp.Request) error {
	switch req.Method {
	case "Setstat":
		// Set file attributes (mode, times, etc.)
		return fs.setstat(req.Filepath, req.Attributes())
		
	case "Rename":
		// Rename/move file
		return fs.Rename(req.Filepath, req.Target)
		
	case "Rmdir":
		// Remove directory
		return fs.rmdir(req.Filepath)
		
	case "Mkdir":
		// Create directory
		attrs := req.Attributes()
		return fs.Mkdir(req.Filepath, os.FileMode(attrs.Mode))
		
	case "Remove":
		// Remove file
		return fs.Remove(req.Filepath)
		
	case "Link":
		// Create hard link (not supported via container exec)
		return fmt.Errorf("hard links not supported")
		
	case "Symlink":
		// Create symbolic link
		return fs.symlink(req.Filepath, req.Target)
		
	default:
		return fmt.Errorf("unsupported command: %s", req.Method)
	}
}

// ContainerLister implements ListerAt for directory listings
type ContainerLister struct {
	files []os.FileInfo
}

func (cl *ContainerLister) ListAt(buffer []os.FileInfo, offset int64) (int, error) {
	if offset >= int64(len(cl.files)) {
		return 0, io.EOF
	}
	
	n := copy(buffer, cl.files[offset:])
	if offset+int64(n) >= int64(len(cl.files)) {
		return n, io.EOF
	}
	
	return n, nil
}

// ContainerReaderAt implements io.ReaderAt for reading files at arbitrary offsets
type ContainerReaderAt struct {
	fs   *ContainerFS
	path string
}

func (r *ContainerReaderAt) ReadAt(p []byte, offset int64) (int, error) {
	containerPath, err := r.fs.resolvePath(r.path)
	if err != nil {
		return 0, err
	}

	ctx := context.Background()
	chunkSize := len(p)
	if chunkSize > 32768 {
		chunkSize = 32768 // Limit chunk size
	}

	// Use dd to read data at specific offset - use sh -c to handle redirection
	ddCmd := fmt.Sprintf("dd if=%s bs=1 count=%d skip=%d 2>/dev/null", 
		containerPath, chunkSize, offset)
	cmd := []string{"sh", "-c", ddCmd}

	output, err := r.fs.execCommand(ctx, cmd)
	if err != nil {
		if strings.Contains(err.Error(), "No such file") {
			return 0, os.ErrNotExist
		}
		return 0, err
	}

	n := copy(p, []byte(output))
	if n == 0 || n < chunkSize {
		return n, io.EOF
	}
	
	return n, nil
}

// ContainerWriterAt implements io.WriterAt for atomic writes
type ContainerWriterAt struct {
	fs            *ContainerFS
	containerPath string
	tempPath      string
	writeBuffer   map[int64][]byte
	maxOffset     int64
}

func (w *ContainerWriterAt) WriteAt(p []byte, offset int64) (int, error) {
	// Buffer the writes - we'll commit them all at once on Close
	data := make([]byte, len(p))
	copy(data, p)
	w.writeBuffer[offset] = data
	
	if offset+int64(len(p)) > w.maxOffset {
		w.maxOffset = offset + int64(len(p))
	}
	
	return len(p), nil
}

func (w *ContainerWriterAt) Close() error {
	// Commit all buffered writes atomically
	return w.commitWrite()
}

func (w *ContainerWriterAt) commitWrite() error {
	if len(w.writeBuffer) == 0 {
		return nil
	}

	ctx := context.Background()

	// Create a complete file from all the WriteAt operations
	completeData := make([]byte, w.maxOffset)
	
	for offset, data := range w.writeBuffer {
		copy(completeData[offset:], data)
	}

	// Write to temp file using base64 encoding for safer shell transfer
	encodedData := base64EncodeForShell(completeData)
	// Use printf instead of echo to avoid line ending issues
	writeCmd := fmt.Sprintf("printf '%%s' '%s' | base64 -d > %s", encodedData, w.tempPath)

	_, err := w.fs.execCommand(ctx, []string{"sh", "-c", writeCmd})
	if err != nil {
		// Ensure temp file is removed on write failure
		w.cleanupTempFile(ctx)
		return fmt.Errorf("failed to write file: %v", err)
	}

	// Atomically move temp file to final location
	_, err = w.fs.execCommand(ctx, []string{"mv", w.tempPath, w.containerPath})
	if err != nil {
		// More aggressive cleanup on move failure
		w.cleanupTempFile(ctx)
		return fmt.Errorf("failed to commit file: %v", err)
	}

	return nil
}

// cleanupTempFile aggressively attempts to remove the temp file
func (w *ContainerWriterAt) cleanupTempFile(ctx context.Context) {
	// Try multiple cleanup approaches to ensure temp file is removed
	cleanupCommands := [][]string{
		{"rm", "-f", w.tempPath},
		{"unlink", w.tempPath},
		{"sh", "-c", fmt.Sprintf("rm -f %s", w.tempPath)},
	}
	
	for _, cmd := range cleanupCommands {
		_, err := w.fs.execCommand(ctx, cmd)
		if err == nil {
			// Success - temp file removed
			return
		}
	}
	
	// If all cleanup attempts failed, log the issue but don't fail the operation
	// In a production system, you might want to add this to a cleanup queue
}

// Helper methods for SFTP operations

func (fs *ContainerFS) setstat(sftpPath string, attrs *sftp.FileStat) error {
	containerPath, err := fs.resolvePath(sftpPath)
	if err != nil {
		return err
	}

	ctx := context.Background()
	
	// Set file mode if specified
	if attrs.Mode != 0 {
		_, err = fs.execCommand(ctx, []string{"chmod", fmt.Sprintf("%o", attrs.Mode), containerPath})
		if err != nil {
			return err
		}
	}

	// Set file times if specified (simplified - would need full implementation)
	// This would involve using 'touch' command with specific timestamps
	
	return nil
}

func (fs *ContainerFS) rmdir(sftpPath string) error {
	containerPath, err := fs.resolvePath(sftpPath)
	if err != nil {
		return err
	}

	ctx := context.Background()
	_, err = fs.execCommand(ctx, []string{"rmdir", containerPath})
	return err
}

func (fs *ContainerFS) symlink(sftpPath, target string) error {
	containerPath, err := fs.resolvePath(sftpPath)
	if err != nil {
		return err
	}

	targetPath, err := fs.resolvePath(target)
	if err != nil {
		return err
	}

	ctx := context.Background()
	_, err = fs.execCommand(ctx, []string{"ln", "-sf", targetPath, containerPath})
	return err
}

// base64EncodeForShell encodes binary data as base64 for safe shell transfer
func base64EncodeForShell(data []byte) string {
	return base64.StdEncoding.EncodeToString(data)
}