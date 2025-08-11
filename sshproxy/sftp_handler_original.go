package sshproxy

// This file contains the ORIGINAL SFTPHandler without the "/" fix
// Used to reproduce the bug in tests

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/pkg/sftp"
)

// OriginalSFTPHandler is the handler WITHOUT the fix for "/"
// This reproduces the bug where scp fails with "close remote: Failure"
type OriginalSFTPHandler struct {
	fs      ContainerFS
	homeDir string
	ctx     context.Context
}

// NewOriginalSFTPHandler creates the buggy handler for testing
func NewOriginalSFTPHandler(ctx context.Context, fs ContainerFS, homeDir string) *OriginalSFTPHandler {
	return &OriginalSFTPHandler{
		fs:      fs,
		homeDir: homeDir,
		ctx:     ctx,
	}
}

func (h *OriginalSFTPHandler) resolvePath(sftpPath string) string {
	// Handle empty path as home directory
	if sftpPath == "" || sftpPath == "." {
		return h.homeDir
	}
	
	// Handle ~ for home directory
	if sftpPath == "~" || sftpPath == "/~" {
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
func (h *OriginalSFTPHandler) Fileread(req *sftp.Request) (io.ReaderAt, error) {
	path := h.resolvePath(req.Filepath)
	
	// Open the file for reading
	file, err := h.fs.OpenFile(h.ctx, path, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	
	return &readerAtWrapper{file: file}, nil
}

// Filewrite implements sftp.FileWriter
func (h *OriginalSFTPHandler) Filewrite(req *sftp.Request) (io.WriterAt, error) {
	originalPath := req.Filepath
	filePath := h.resolvePath(originalPath)
	
	// Check if the path points to an existing directory
	info, err := h.fs.Stat(h.ctx, filePath)
	if err == nil && info.IsDir() {
		// Return directory wrapper for existing directories
		return &directoryUploadWrapper{
			fs:       h.fs,
			ctx:      h.ctx,
			dirPath:  filePath,
			origPath: originalPath,
		}, nil
	}
	
	// NO special handling for "/" - this is the bug!
	// When SCP sends "/", we try to create a file at "/" which fails on Close
	
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
func (h *OriginalSFTPHandler) Filecmd(req *sftp.Request) error {
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
		return h.symlink(req.Filepath, req.Target)
	case "Link":
		// Hard links not supported
		return nil
	default:
		return nil
	}
}

// Filelist implements sftp.FileLister
func (h *OriginalSFTPHandler) Filelist(req *sftp.Request) (sftp.ListerAt, error) {
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
		return nil, nil
	}
}

// Helper methods
func (h *OriginalSFTPHandler) list(sftpPath string) (sftp.ListerAt, error) {
	path := h.resolvePath(sftpPath)
	entries, err := h.fs.ReadDir(h.ctx, path)
	if err != nil {
		return nil, err
	}
	return &listerAt{entries: entries}, nil
}

func (h *OriginalSFTPHandler) stat(sftpPath string) (sftp.ListerAt, error) {
	path := h.resolvePath(sftpPath)
	info, err := h.fs.Stat(h.ctx, path)
	if err != nil {
		return nil, err
	}
	return &listerAt{entries: []os.FileInfo{info}}, nil
}

func (h *OriginalSFTPHandler) lstat(sftpPath string) (sftp.ListerAt, error) {
	path := h.resolvePath(sftpPath)
	info, err := h.fs.Lstat(h.ctx, path)
	if err != nil {
		return nil, err
	}
	return &listerAt{entries: []os.FileInfo{info}}, nil
}

func (h *OriginalSFTPHandler) readlink(sftpPath string) (sftp.ListerAt, error) {
	path := h.resolvePath(sftpPath)
	target, err := h.fs.Readlink(h.ctx, path)
	if err != nil {
		return nil, err
	}
	info := &linkInfo{name: target}
	return &listerAt{entries: []os.FileInfo{info}}, nil
}

func (h *OriginalSFTPHandler) setstat(sftpPath string, attrs *sftp.FileStat) error {
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
		h.fs.Chown(h.ctx, path, int(attrs.UID), int(attrs.GID))
	}
	
	return nil
}

func (h *OriginalSFTPHandler) rename(oldSftpPath, newSftpPath string) error {
	oldPath := h.resolvePath(oldSftpPath)
	newPath := h.resolvePath(newSftpPath)
	return h.fs.Rename(h.ctx, oldPath, newPath)
}

func (h *OriginalSFTPHandler) remove(sftpPath string) error {
	path := h.resolvePath(sftpPath)
	return h.fs.Remove(h.ctx, path)
}

func (h *OriginalSFTPHandler) mkdir(sftpPath string, attrs *sftp.FileStat) error {
	path := h.resolvePath(sftpPath)
	mode := os.FileMode(0755)
	if attrs.Mode != 0 {
		mode = os.FileMode(attrs.Mode)
	}
	return h.fs.Mkdir(h.ctx, path, mode)
}

func (h *OriginalSFTPHandler) rmdir(sftpPath string) error {
	path := h.resolvePath(sftpPath)
	return h.fs.Remove(h.ctx, path)
}

func (h *OriginalSFTPHandler) symlink(targetPath, linkPath string) error {
	link := h.resolvePath(linkPath)
	return h.fs.Symlink(h.ctx, targetPath, link)
}