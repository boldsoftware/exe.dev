//go:build !linux

package storage

import (
	"fmt"
	"log/slog"
	"runtime"
)

// NewStorageManager returns a new storage manager of the specified type
func NewStorageManager(addr string, log *slog.Logger) (StorageManager, error) {
	return nil, fmt.Errorf("storage manager not supported on %s", runtime.GOOS)
}
