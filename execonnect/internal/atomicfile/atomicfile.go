// Package atomicfile durably replaces files without exposing partial writes.
package atomicfile

import (
	"fmt"
	"os"
	"path/filepath"
)

// WriteFileSync writes data through a temporary sibling, fsyncs it, atomically
// replaces path, and fsyncs the containing directory.
func WriteFileSync(path string, data []byte, perm os.FileMode) error {
	dir, base := filepath.Split(path)
	file, err := os.CreateTemp(dir, base+".tmp*")
	if err != nil {
		return fmt.Errorf("atomic create tmp: %w", err)
	}
	tmpPath := file.Name()
	cleanup := func() {
		file.Close()
		os.Remove(tmpPath)
	}
	if _, err := file.Write(data); err != nil {
		cleanup()
		return fmt.Errorf("atomic write tmp: %w", err)
	}
	if err := file.Chmod(perm); err != nil {
		cleanup()
		return fmt.Errorf("atomic chmod tmp: %w", err)
	}
	if err := file.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("atomic sync tmp: %w", err)
	}
	if err := file.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("atomic close tmp: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("atomic rename: %w", err)
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("atomic open dir: %w", err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("atomic sync dir: %w", err)
	}
	return nil
}
