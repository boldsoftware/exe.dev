package zfs

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"sync"
)

const (
	storageType       = "zfs"
	encryptionKeyName = "encryption.key"
)

// ErrNotImplemented is returned for functionality that is not implemented
var ErrNotImplemented = errors.New("not implemented")

type ZFS struct {
	dataDir string
	dsName  string
	mu      *sync.Mutex
	log     *slog.Logger
}

// NewZFS returns a new ZFS based storage manager
func NewZFS(addr string, log *slog.Logger) (*ZFS, error) {
	u, err := url.Parse(addr)
	if err != nil {
		return nil, fmt.Errorf("error loading storage manager: %w", err)
	}
	if !strings.EqualFold(u.Scheme, storageType) {
		return nil, fmt.Errorf("invalid config specified for ZFS storage manager: %s", addr)
	}

	// data
	dataDir := u.Path
	dsName := ""
	if v := u.Query().Get("dataset"); v != "" {
		dsName = v
	}

	// create data dir
	if err := os.MkdirAll(dataDir, 0o770); err != nil {
		return nil, fmt.Errorf("error creating zfs storage manager data dir: %w", err)
	}

	if dsName == "" {
		return nil, fmt.Errorf("zfs storage manager dataset name cannot be blank")
	}

	log.Debug("loaded zfs storage manager", "ds", dsName)

	return &ZFS{
		dataDir: dataDir,
		dsName:  dsName,
		mu:      &sync.Mutex{},
		log:     log,
	}, nil
}

func (s *ZFS) Type() string {
	return storageType
}
