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
	cPath := l.contentPath(desc)
	slog.DebugContext(ctx, "storing content", "digest", desc.Digest.Encoded(), "path", cPath)

	// Ensure parent directory exists
	if err := os.MkdirAll(filepath.Dir(cPath), 0o755); err != nil {
		return err
	}

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
	return filepath.Join(l.dataDir, "blobs", desc.Digest.Algorithm().String(), desc.Digest.Encoded())
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
	// Use blobs/sha256 directory for temp files (same location as final content)
	ingestDir := filepath.Join(l.dataDir, "blobs", "sha256")
	if err := os.MkdirAll(ingestDir, 0o755); err != nil {
		return nil, err
	}
	return os.CreateTemp(ingestDir, ".tmp-*")
}

// checkContent checks if content exists on disk for a given digest and returns its size
func (l *LocalStore) checkContent(d digest.Digest) (int64, bool) {
	path := filepath.Join(l.dataDir, "blobs", d.Algorithm().String(), d.Encoded())
	slog.Debug("checking for content", "digest", d.Encoded(), "path", path)

	info, err := os.Stat(path)
	if err != nil {
		return 0, false
	}

	return info.Size(), true
}
