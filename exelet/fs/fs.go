package exelet

import (
	"embed"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// Content is initialized from arch-specific embed in fs_amd64.go or fs_arm64.go.
// The fs.Sub strips the architecture prefix so paths remain "kernel/kernel", "rovol/bin/...", etc.
var Content fs.FS

//go:embed rovol
var sharedContent embed.FS

func init() {
	var err error
	Content, err = fs.Sub(archContent, archDir)
	if err != nil {
		panic("exelet/fs: failed to create sub-filesystem: " + err.Error())
	}
	f, err := Content.Open("rovol/bin/exe-init")
	if err != nil {
		panic("exelet/fs: exe-init missing from embedded filesystem (run 'make exe-init')")
	}
	f.Close()
}

// Get returns the specified file from the fs
func Get(name string) (fs.File, error) {
	return Content.Open(name)
}

// Kernel returns the exelet default kernel
func Kernel() (fs.File, error) {
	return Content.Open("kernel/kernel")
}

// CopyRovol copies the exe.dev rovol to the destination.
// It first copies the arch-specific binaries, then overlays the shared configs.
func CopyRovol(target string) error {
	if err := copyTree(Content, "rovol", target); err != nil {
		return err
	}
	return copyTree(sharedContent, "rovol", target)
}

func copyTree(fsys fs.FS, root, target string) error {
	return fs.WalkDir(fsys, root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if path == "." {
			return nil
		}

		destPath := filepath.Join(target, strings.TrimPrefix(path, root))
		if d.IsDir() {
			slog.Info("making rovol path", "dest", destPath)
			return os.MkdirAll(destPath, 0o755)
		}

		return copyFile(fsys, path, destPath)
	})
}

func copyFile(fsys fs.FS, src, dest string) error {
	// info to match perms
	info, err := fs.Stat(fsys, src)
	if err != nil {
		return err
	}

	srcFile, err := fsys.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	destFile, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode())
	if err != nil {
		return err
	}
	defer destFile.Close()

	if _, err := io.Copy(destFile, srcFile); err != nil {
		return err
	}

	// check if in bin and make executable
	if strings.Contains(dest, "/bin/") || strings.Contains(dest, "/lib/") {
		if err := os.Chmod(dest, 0o755); err != nil {
			return err
		}
	}

	// fixup perms
	if err := fixupPermissions(dest); err != nil {
		return err
	}

	return destFile.Sync()
}

// fixupPermissions sets permissions for specific contents
// because embed.FS stores without mode for security
func fixupPermissions(dest string) error {
	type perm struct {
		mode int
		uid  int
		gid  int
	}

	perms := map[string]perm{
		// must be owned by root
		"sshd_config": {
			mode: 0o600,
			uid:  0,
			gid:  0,
		},
	}

	fname := filepath.Base(dest)
	if perm, ok := perms[fname]; ok {
		slog.Info("setting mode", "name", fname, "path", dest, "mode", perm.mode)
		if err := os.Chmod(dest, os.FileMode(perm.mode)); err != nil {
			return err
		}
		slog.Info("setting ownership", "name", fname, "path", dest, "uid", perm.uid, "gid", perm.gid)
		if err := os.Chown(dest, perm.uid, perm.gid); err != nil {
			return err
		}
	}
	return nil
}
