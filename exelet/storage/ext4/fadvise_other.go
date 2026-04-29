//go:build !linux

package ext4

import (
	"fmt"
	"os"
)

// readSuperblockUncached on non-Linux platforms is a plain buffered
// read. There's no portable way to bypass the page cache, but exelet
// only runs on Linux so this exists just to keep the package
// compilable for cross-platform tooling.
func readSuperblockUncached(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		return nil, fmt.Errorf("ext4: open %s: %w", path, err)
	}
	defer f.Close()
	buf := make([]byte, superblockSize)
	if _, err := f.ReadAt(buf, SuperblockOffset); err != nil {
		return nil, fmt.Errorf("ext4: read superblock: %w", err)
	}
	return buf, nil
}
