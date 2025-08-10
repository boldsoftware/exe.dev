package sshproxy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/pkg/sftp"
)

// mockContainerFS implements ContainerFS for testing
type mockContainerFS struct {
	files map[string]*mockFile
}

type mockFile struct {
	content []byte
	mode    os.FileMode
	mtime   time.Time
	isDir   bool
}

func newMockContainerFS() *mockContainerFS {
	fs := &mockContainerFS{
		files: make(map[string]*mockFile),
	}
	// Create home directory
	fs.files["/workspace"] = &mockFile{
		isDir: true,
		mode:  os.ModeDir | 0755,
		mtime: time.Now(),
	}
	return fs
}

func (m *mockContainerFS) Stat(ctx context.Context, path string) (os.FileInfo, error) {
	file, ok := m.files[path]
	if !ok {
		return nil, os.ErrNotExist
	}
	return &mockFileInfo{
		name:  path,
		size:  int64(len(file.content)),
		mode:  file.mode,
		mtime: file.mtime,
		isDir: file.isDir,
	}, nil
}

func (m *mockContainerFS) ReadDir(ctx context.Context, path string) ([]os.FileInfo, error) {
	var entries []os.FileInfo
	for filePath, file := range m.files {
		if strings.HasPrefix(filePath, path+"/") {
			// Check if it's a direct child
			relativePath := strings.TrimPrefix(filePath, path+"/")
			if !strings.Contains(relativePath, "/") {
				entries = append(entries, &mockFileInfo{
					name:  relativePath,
					size:  int64(len(file.content)),
					mode:  file.mode,
					mtime: file.mtime,
					isDir: file.isDir,
				})
			}
		}
	}
	return entries, nil
}

func (m *mockContainerFS) OpenFile(ctx context.Context, path string, flags int, mode os.FileMode) (File, error) {
	if flags&os.O_CREATE != 0 {
		if _, exists := m.files[path]; !exists {
			m.files[path] = &mockFile{
				mode:  mode,
				mtime: time.Now(),
			}
		}
	}
	
	file, ok := m.files[path]
	if !ok && flags&os.O_CREATE == 0 {
		return nil, os.ErrNotExist
	}
	
	return &mockFileHandle{
		fs:    m,
		path:  path,
		file:  file,
		flags: flags,
	}, nil
}

func (m *mockContainerFS) Open(ctx context.Context, path string) (io.ReadCloser, error) {
	return m.OpenFile(ctx, path, os.O_RDONLY, 0)
}

func (m *mockContainerFS) Create(ctx context.Context, path string) (io.WriteCloser, error) {
	return m.OpenFile(ctx, path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
}

func (m *mockContainerFS) Mkdir(ctx context.Context, path string, mode os.FileMode) error {
	m.files[path] = &mockFile{
		isDir: true,
		mode:  mode | os.ModeDir,
		mtime: time.Now(),
	}
	return nil
}

func (m *mockContainerFS) Remove(ctx context.Context, path string) error {
	delete(m.files, path)
	return nil
}

func (m *mockContainerFS) RemoveAll(ctx context.Context, path string) error {
	for filePath := range m.files {
		if strings.HasPrefix(filePath, path) {
			delete(m.files, filePath)
		}
	}
	return nil
}

func (m *mockContainerFS) Rename(ctx context.Context, oldPath, newPath string) error {
	file, ok := m.files[oldPath]
	if !ok {
		return os.ErrNotExist
	}
	m.files[newPath] = file
	delete(m.files, oldPath)
	return nil
}

func (m *mockContainerFS) Symlink(ctx context.Context, target, link string) error {
	m.files[link] = &mockFile{
		content: []byte(target),
		mode:    os.ModeSymlink,
		mtime:   time.Now(),
	}
	return nil
}

func (m *mockContainerFS) Readlink(ctx context.Context, path string) (string, error) {
	file, ok := m.files[path]
	if !ok {
		return "", os.ErrNotExist
	}
	if file.mode&os.ModeSymlink == 0 {
		return "", fmt.Errorf("not a symlink")
	}
	return string(file.content), nil
}

func (m *mockContainerFS) Chmod(ctx context.Context, path string, mode os.FileMode) error {
	file, ok := m.files[path]
	if !ok {
		return os.ErrNotExist
	}
	file.mode = mode
	return nil
}

func (m *mockContainerFS) Chown(ctx context.Context, path string, uid, gid int) error {
	// No-op for testing
	return nil
}

func (m *mockContainerFS) Chtimes(ctx context.Context, path string, atime, mtime int64) error {
	file, ok := m.files[path]
	if !ok {
		return os.ErrNotExist
	}
	file.mtime = time.Unix(mtime, 0)
	return nil
}

// mockFileInfo implements os.FileInfo
type mockFileInfo struct {
	name  string
	size  int64
	mode  os.FileMode
	mtime time.Time
	isDir bool
}

func (m *mockFileInfo) Name() string       { return m.name }
func (m *mockFileInfo) Size() int64        { return m.size }
func (m *mockFileInfo) Mode() os.FileMode  { return m.mode }
func (m *mockFileInfo) ModTime() time.Time { return m.mtime }
func (m *mockFileInfo) IsDir() bool        { return m.isDir }
func (m *mockFileInfo) Sys() interface{}   { return nil }

// mockFileHandle implements File
type mockFileHandle struct {
	fs       *mockContainerFS
	path     string
	file     *mockFile
	flags    int
	position int64
	buffer   bytes.Buffer
}

func (m *mockFileHandle) Read(p []byte) (int, error) {
	if m.file == nil {
		return 0, os.ErrNotExist
	}
	return copy(p, m.file.content[m.position:]), nil
}

func (m *mockFileHandle) ReadAt(p []byte, off int64) (int, error) {
	if m.file == nil {
		return 0, os.ErrNotExist
	}
	if off >= int64(len(m.file.content)) {
		return 0, io.EOF
	}
	n := copy(p, m.file.content[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func (m *mockFileHandle) Write(p []byte) (int, error) {
	return m.buffer.Write(p)
}

func (m *mockFileHandle) WriteAt(p []byte, off int64) (int, error) {
	// Simple implementation - just append for now
	return m.buffer.Write(p)
}

func (m *mockFileHandle) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		m.position = offset
	case io.SeekCurrent:
		m.position += offset
	case io.SeekEnd:
		m.position = int64(len(m.file.content)) + offset
	}
	return m.position, nil
}

func (m *mockFileHandle) Close() error {
	if m.buffer.Len() > 0 {
		m.file.content = m.buffer.Bytes()
	}
	return nil
}

func (m *mockFileHandle) Stat() (os.FileInfo, error) {
	return &mockFileInfo{
		name:  m.path,
		size:  int64(len(m.file.content)),
		mode:  m.file.mode,
		mtime: m.file.mtime,
		isDir: m.file.isDir,
	}, nil
}

func (m *mockFileHandle) Sync() error {
	return nil
}

func (m *mockFileHandle) Truncate(size int64) error {
	if size < int64(len(m.file.content)) {
		m.file.content = m.file.content[:size]
	}
	return nil
}

// Tests

func TestSFTPHandlerPathResolution(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"empty path", "", "/workspace"},
		{"dot", ".", "/workspace"},
		{"tilde", "~", "/workspace"},
		{"tilde with file", "~/file.txt", "/workspace/file.txt"},
		{"relative path", "file.txt", "/workspace/file.txt"},
		{"relative subdir", "dir/file.txt", "/workspace/dir/file.txt"},
		{"absolute path", "/tmp/file.txt", "/tmp/file.txt"},
		{"absolute workspace", "/workspace/file.txt", "/workspace/file.txt"},
	}
	
	ctx := context.Background()
	fs := newMockContainerFS()
	handler := NewSFTPHandler(ctx, fs, "/workspace")
	
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := handler.resolvePath(tt.input)
			if result != tt.expected {
				t.Errorf("resolvePath(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestSFTPFileUpload(t *testing.T) {
	ctx := context.Background()
	fs := newMockContainerFS()
	handler := NewSFTPHandler(ctx, fs, "/workspace")
	
	// Test cases for file upload paths
	tests := []struct {
		name         string
		sftpPath     string
		expectError  bool
		expectedPath string
	}{
		{
			name:         "upload to home with filename",
			sftpPath:     "~/test.txt",
			expectError:  false,
			expectedPath: "/workspace/test.txt",
		},
		{
			name:         "upload with relative path",
			sftpPath:     "test.txt",
			expectError:  false,
			expectedPath: "/workspace/test.txt",
		},
		{
			name:         "upload with absolute path",
			sftpPath:     "/workspace/test.txt",
			expectError:  false,
			expectedPath: "/workspace/test.txt",
		},
		{
			name:        "upload to directory without filename",
			sftpPath:    "/workspace",
			expectError: true, // Should fail - can't write to directory
		},
		{
			name:        "upload to tilde alone",
			sftpPath:    "~",
			expectError: true, // Should fail - can't write to directory
		},
		{
			name:        "upload to dot",
			sftpPath:    ".",
			expectError: true, // Should fail - can't write to directory
		},
	}
	
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &sftp.Request{
				Method:   "Put",
				Filepath: tt.sftpPath,
			}
			
			writer, err := handler.Filewrite(req)
			
			if tt.expectError {
				if err == nil {
					t.Errorf("Expected error for path %q, got nil", tt.sftpPath)
				}
			} else {
				if err != nil {
					t.Errorf("Unexpected error for path %q: %v", tt.sftpPath, err)
				}
				
				// Write some data
				if writer != nil {
					testData := []byte("test content")
					n, err := writer.WriteAt(testData, 0)
					if err != nil {
						t.Errorf("WriteAt failed: %v", err)
					}
					if n != len(testData) {
						t.Errorf("WriteAt wrote %d bytes, expected %d", n, len(testData))
					}
					
					// Close the writer
					if closer, ok := writer.(io.Closer); ok {
						closer.Close()
					}
					
					// Verify file was created at expected path
					if _, exists := fs.files[tt.expectedPath]; !exists {
						t.Errorf("File not created at expected path %q", tt.expectedPath)
					}
				}
			}
		})
	}
}

func TestSFTPDirectoryOperations(t *testing.T) {
	ctx := context.Background()
	fs := newMockContainerFS()
	handler := NewSFTPHandler(ctx, fs, "/workspace")
	
	// Create a directory
	err := handler.mkdir("testdir", &sftp.FileStat{Mode: 0755})
	if err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}
	
	// Check directory was created
	expectedPath := "/workspace/testdir"
	file, exists := fs.files[expectedPath]
	if !exists {
		t.Errorf("Directory not created at %q", expectedPath)
	}
	if !file.isDir {
		t.Errorf("Created file is not a directory")
	}
	
	// List directory contents
	lister, err := handler.list(".")
	if err != nil {
		t.Fatalf("list failed: %v", err)
	}
	
	var entries []os.FileInfo
	for {
		batch := make([]os.FileInfo, 10)
		n, err := lister.ListAt(batch, int64(len(entries)))
		entries = append(entries, batch[:n]...)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("ListAt failed: %v", err)
		}
	}
	
	// Should have at least the testdir
	found := false
	for _, entry := range entries {
		if entry.Name() == "testdir" && entry.IsDir() {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Created directory not found in listing")
	}
	
	// Remove directory
	err = handler.rmdir("testdir")
	if err != nil {
		t.Fatalf("rmdir failed: %v", err)
	}
	
	// Check directory was removed
	if _, exists := fs.files[expectedPath]; exists {
		t.Errorf("Directory not removed")
	}
}

func TestSFTPRename(t *testing.T) {
	ctx := context.Background()
	fs := newMockContainerFS()
	handler := NewSFTPHandler(ctx, fs, "/workspace")
	
	// Create a file
	oldPath := "/workspace/old.txt"
	fs.files[oldPath] = &mockFile{
		content: []byte("test content"),
		mode:    0644,
		mtime:   time.Now(),
	}
	
	// Rename file
	err := handler.rename("old.txt", "new.txt")
	if err != nil {
		t.Fatalf("rename failed: %v", err)
	}
	
	// Check old file is gone
	if _, exists := fs.files[oldPath]; exists {
		t.Errorf("Old file still exists")
	}
	
	// Check new file exists
	newPath := "/workspace/new.txt"
	file, exists := fs.files[newPath]
	if !exists {
		t.Errorf("New file doesn't exist")
	}
	if string(file.content) != "test content" {
		t.Errorf("File content changed during rename")
	}
}

func TestSFTPSymlink(t *testing.T) {
	ctx := context.Background()
	fs := newMockContainerFS()
	handler := NewSFTPHandler(ctx, fs, "/workspace")
	
	// Create a target file
	targetPath := "/workspace/target.txt"
	fs.files[targetPath] = &mockFile{
		content: []byte("target content"),
		mode:    0644,
		mtime:   time.Now(),
	}
	
	// Create symlink
	err := handler.symlink("target.txt", "link.txt")
	if err != nil {
		t.Fatalf("symlink failed: %v", err)
	}
	
	// Check symlink exists
	linkPath := "/workspace/link.txt"
	link, exists := fs.files[linkPath]
	if !exists {
		t.Errorf("Symlink not created")
	}
	if link.mode&os.ModeSymlink == 0 {
		t.Errorf("Created file is not a symlink")
	}
	
	// Read symlink
	lister, err := handler.readlink("link.txt")
	if err != nil {
		t.Fatalf("readlink failed: %v", err)
	}
	
	entries := make([]os.FileInfo, 1)
	n, _ := lister.ListAt(entries, 0)
	if n > 0 {
		// The Name() should return the target path
		if entries[0].Name() != "/workspace/target.txt" {
			t.Errorf("Symlink target incorrect: %q", entries[0].Name())
		}
	}
}

func TestSFTPPermissions(t *testing.T) {
	ctx := context.Background()
	fs := newMockContainerFS()
	handler := NewSFTPHandler(ctx, fs, "/workspace")
	
	// Create a file
	filePath := "/workspace/test.txt"
	fs.files[filePath] = &mockFile{
		content: []byte("test"),
		mode:    0644,
		mtime:   time.Now(),
	}
	
	// Change permissions
	err := handler.setstat("test.txt", &sftp.FileStat{
		Mode:  0755,
		Atime: uint32(time.Now().Unix()),
		Mtime: uint32(time.Now().Unix()),
	})
	if err != nil {
		t.Fatalf("setstat failed: %v", err)
	}
	
	// Check permissions changed
	file := fs.files[filePath]
	if file.mode != 0755 {
		t.Errorf("Mode not changed: got %o, want %o", file.mode, 0755)
	}
}