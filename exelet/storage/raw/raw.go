package raw

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	storageType     = "raw"
	diskName        = "disk.img"
	defaultStateDir = "/run/exe/storage/raw"
)

// ErrNotImplemented is returned for functionality that is not implemented
var ErrNotImplemented = errors.New("not implemented")

type Raw struct {
	dataDir  string
	stateDir string
	mu       *sync.Mutex
	log      *slog.Logger
}

// NewRaw returns a new raw based storage manager
func NewRaw(addr string, log *slog.Logger) (*Raw, error) {
	u, err := url.Parse(addr)
	if err != nil {
		return nil, fmt.Errorf("error loading storage manager: %w", err)
	}
	if !strings.EqualFold(u.Scheme, storageType) {
		return nil, fmt.Errorf("invalid config specified for raw storage manager: %s", addr)
	}

	// data
	dataDir := u.Path

	// create data dir
	if err := os.MkdirAll(dataDir, 0o770); err != nil {
		return nil, fmt.Errorf("error creating raw storage manager data dir: %w", err)
	}

	stateDir := defaultStateDir
	if v := u.Query().Get("state-dir"); v != "" {
		stateDir = v
	}

	if err := os.MkdirAll(stateDir, 0o770); err != nil {
		return nil, fmt.Errorf("error creating raw storage state dir %s: %w", stateDir, err)
	}

	log.Debug("loaded raw storage manager", "datadir", dataDir)

	return &Raw{
		dataDir:  dataDir,
		stateDir: stateDir,
		mu:       &sync.Mutex{},
		log:      log,
	}, nil
}

func (s *Raw) Type() string {
	return storageType
}

func (s *Raw) getInstanceDir(id string) (string, error) {
	p := filepath.Join(s.dataDir, "volumes", id)
	if err := os.MkdirAll(filepath.Dir(p), 0o770); err != nil {
		return "", err
	}
	return p, nil
}

func (s *Raw) getInstanceDiskPath(id string) (string, error) {
	p, err := s.getInstanceDir(id)
	if err != nil {
		return "", err
	}
	diskPath := filepath.Join(p, diskName)
	if err := os.MkdirAll(filepath.Dir(diskPath), 0o770); err != nil {
		return "", err
	}
	return diskPath, nil
}

func (s *Raw) getInstanceStatePath(id string) string {
	return filepath.Join(s.stateDir, id)
}

func (s *Raw) getInstanceFSMountpoint(id string) (string, error) {
	p := filepath.Join(s.dataDir, "mounts", id)
	if err := os.MkdirAll(filepath.Dir(p), 0o770); err != nil {
		return "", err
	}
	return p, nil
}
