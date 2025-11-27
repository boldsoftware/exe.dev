package store

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

type LocalStore struct {
	dataDir string
}

// NewLocalStore returns a local content store
func NewLocalStore(dataDir string) *LocalStore {
	return &LocalStore{
		dataDir: dataDir,
	}
}

// Fetch retrieves the specified content from the store
func (l *LocalStore) Fetch(ctx context.Context, desc ocispec.Descriptor) (io.ReadCloser, error) {
	cPath := l.contentPath(desc)
	slog.DebugContext(ctx, "fetching content", "digest", desc.Digest.Encoded(), "path", cPath)
	if _, err := os.Stat(cPath); err != nil {
		return nil, fmt.Errorf("content not found for %s", desc.Digest.Encoded())
	}
	return os.Open(cPath)
}

// Push persists the specified content in the store
func (l *LocalStore) Push(ctx context.Context, desc ocispec.Descriptor, content io.Reader) error {
	slog.DebugContext(ctx, "storing content", "digest", desc.Digest.Encoded(), "path", l.dataDir)
	cPath := l.contentPath(desc)
	// remove existing
	_ = os.Remove(cPath)

	f, err := os.Create(cPath)
	if err != nil {
		return err
	}
	defer f.Close()

	if _, err := io.Copy(f, content); err != nil {
		return err
	}

	return nil
}

func (l *LocalStore) contentPath(desc ocispec.Descriptor) string {
	return filepath.Join(l.dataDir, desc.Digest.Encoded())
}

// openFile opens a file for reading with ReaderAt support
func (l *LocalStore) openFile(path string) (*os.File, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("content not found: %s", path)
	}
	return os.Open(path)
}

// createTempFile creates a temporary file for writing content
func (l *LocalStore) createTempFile() (*os.File, error) {
	// Ensure the data directory exists
	if err := os.MkdirAll(l.dataDir, 0o755); err != nil {
		return nil, err
	}
	// Create a temp file in the data directory
	return os.CreateTemp(l.dataDir, ".tmp-*")
}

// checkContent checks if content exists on disk for a given digest and returns its size
func (l *LocalStore) checkContent(d digest.Digest) (int64, bool) {
	slog.Debug("checking for content", "digest", d.Encoded(), "path", l.dataDir)
	// Build path using a descriptor with this digest
	path := filepath.Join(l.dataDir, d.Encoded())

	info, err := os.Stat(path)
	if err != nil {
		return 0, false
	}

	return info.Size(), true
}
