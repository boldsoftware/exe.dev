// Package templates provides embedded project templates for shelley.
package templates

import (
	"embed"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

//go:embed go
var FS embed.FS

// List returns the names of all available templates.
func List() ([]string, error) {
	entries, err := FS.ReadDir(".")
	if err != nil {
		return nil, fmt.Errorf("read templates dir: %w", err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return names, nil
}

// Unpack extracts the named template to the given directory.
// The directory must exist and should be empty.
func Unpack(templateName, destDir string) error {
	// Check that the template exists
	_, err := FS.ReadDir(templateName)
	if err != nil {
		return fmt.Errorf("template %q not found: %w", templateName, err)
	}

	// Walk the embedded filesystem and copy files
	return fs.WalkDir(FS, templateName, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		// Get the relative path from the template directory
		relPath, err := filepath.Rel(templateName, path)
		if err != nil {
			return fmt.Errorf("get relative path: %w", err)
		}

		// Skip the root directory itself
		if relPath == "." {
			return nil
		}

		// Handle .template suffix - rename go.mod.template -> go.mod, etc.
		// This allows us to embed directories containing go.mod files
		// (which are otherwise treated as separate modules by go:embed)
		if strings.HasSuffix(relPath, ".template") {
			relPath = strings.TrimSuffix(relPath, ".template")
		}

		target := filepath.Join(destDir, relPath)

		if d.IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return fmt.Errorf("mkdir %s: %w", target, err)
			}
			return nil
		}

		// It's a file - read from embedded FS and write to disk
		srcFile, err := FS.Open(path)
		if err != nil {
			return fmt.Errorf("open embedded file %s: %w", path, err)
		}
		defer srcFile.Close()

		// Get file info for permissions
		info, err := d.Info()
		if err != nil {
			return fmt.Errorf("get file info %s: %w", path, err)
		}

		mode := info.Mode()
		if mode == 0 {
			mode = 0o644
		}

		// Ensure parent directory exists
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("mkdir for %s: %w", target, err)
		}

		// Create the file
		dstFile, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
		if err != nil {
			return fmt.Errorf("create %s: %w", target, err)
		}
		defer dstFile.Close()

		if _, err := io.Copy(dstFile, srcFile); err != nil {
			return fmt.Errorf("write %s: %w", target, err)
		}

		return nil
	})
}
