package atomicfile

import (
	"fmt"
	"os"
)

// WriteFile atomically writes data to a file by first writing to a temporary
// sibling file and then renaming. This is crash-safe on Linux when source and
// destination are on the same filesystem (which is always the case for .tmp siblings).
func WriteFile(path string, data []byte, perm os.FileMode) error {
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, perm); err != nil {
		return fmt.Errorf("atomic write tmp: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("atomic rename: %w", err)
	}
	return nil
}
