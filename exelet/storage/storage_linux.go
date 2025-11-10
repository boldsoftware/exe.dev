//go:build linux

package storage

import (
	"fmt"
	"log/slog"
	"net/url"
	"strings"

	"exe.dev/exelet/storage/zfs"
)

// NewStorageManager returns a new storage manager of the specified type
func NewStorageManager(addr string, log *slog.Logger) (StorageManager, error) {
	u, err := url.Parse(addr)
	if err != nil {
		return nil, err
	}
	switch strings.TrimSpace(strings.ToLower(u.Scheme)) {
	case "zfs":
		return zfs.NewZFS(addr, log)
	}

	return nil, fmt.Errorf("unsupported secret store type %q", u.Scheme)
}
