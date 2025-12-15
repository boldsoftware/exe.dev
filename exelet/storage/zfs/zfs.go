//go:build linux

package zfs

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"sort"
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
	locks   sync.Map // map[string]*sync.Mutex - per-volume locks
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
		log:     log,
	}, nil
}

func (s *ZFS) Type() string {
	return storageType
}

// getLock returns the lock for a volume ID, creating one if needed
func (s *ZFS) getLock(id string) *sync.Mutex {
	lock, _ := s.locks.LoadOrStore(id, &sync.Mutex{})
	return lock.(*sync.Mutex)
}

// lockVolume locks a single volume and returns an unlock function
func (s *ZFS) lockVolume(id string) func() {
	lock := s.getLock(id)
	lock.Lock()
	return lock.Unlock
}

// lockVolumes locks multiple volumes in sorted order to prevent deadlocks
// and returns an unlock function that releases them in reverse order
func (s *ZFS) lockVolumes(ids ...string) func() {
	sorted := make([]string, len(ids))
	copy(sorted, ids)
	sort.Strings(sorted)

	unlocks := make([]func(), len(sorted))
	for i, id := range sorted {
		lock := s.getLock(id)
		lock.Lock()
		unlocks[i] = lock.Unlock
	}

	return func() {
		for i := len(unlocks) - 1; i >= 0; i-- {
			unlocks[i]()
		}
	}
}

func align4K(v uint64) uint64 {
	blockSize := uint64(4 * 1024)
	return uint64(((v + blockSize - 1) / blockSize) * blockSize)
}
