package store

import (
	"bytes"
	"context"
	"io"
	"os"
	"sync"
	"time"

	ccontent "github.com/containerd/containerd/content"
	"github.com/containerd/errdefs"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
)

// ensure interface
var (
	_ ccontent.Manager  = &ContentStore{}
	_ ccontent.Provider = &ContentStore{}
	_ ccontent.Ingester = &ContentStore{}
	_ ccontent.ReaderAt = sizeReaderAt{}
)

type labelStore struct {
	l      sync.RWMutex
	labels map[digest.Digest]map[string]string
}

// ContentStore implements a simple in-memory content store for labels and
// descriptors (and associated content for manifests and configs)
type ContentStore struct {
	store   *LocalStore
	labels  labelStore
	nameMap map[string]ocispec.Descriptor
}

func newLabelStore() labelStore {
	return labelStore{
		labels: map[digest.Digest]map[string]string{},
	}
}

// NewContentStore creates a memory store that implements the proper
// content interfaces to support simple push/inspect operations on
// containerd's content in a memory-only context
func NewContentStore(dataDir string) (*ContentStore, error) {
	ts := NewLocalStore(dataDir)
	return &ContentStore{
		store:   ts,
		labels:  newLabelStore(),
		nameMap: map[string]ocispec.Descriptor{},
	}, nil
}

// Update updates mutable label field content related to a descriptor
func (m *ContentStore) Update(ctx context.Context, info ccontent.Info, fieldpaths ...string) (ccontent.Info, error) {
	newLabels, err := m.update(info.Digest, info.Labels)
	if err != nil {
		return ccontent.Info{}, nil
	}
	info.Labels = newLabels
	return info, nil
}

// Walk is unimplemented
func (m *ContentStore) Walk(ctx context.Context, fn ccontent.WalkFunc, filters ...string) error {
	// unimplemented
	return nil
}

func (m *ContentStore) update(d digest.Digest, update map[string]string) (map[string]string, error) {
	m.labels.l.Lock()
	labels, ok := m.labels.labels[d]
	if !ok {
		labels = map[string]string{}
	}
	for k, v := range update {
		if v == "" {
			delete(labels, k)
		} else {
			labels[k] = v
		}
	}
	m.labels.labels[d] = labels
	m.labels.l.Unlock()

	return labels, nil
}

// Delete is unimplemented as we don't use it in the flow of manifest-tool
func (m *ContentStore) Delete(ctx context.Context, d digest.Digest) error {
	return nil
}

// Info returns the info for a specific digest
func (m *ContentStore) Info(ctx context.Context, d digest.Digest) (ccontent.Info, error) {
	// Check if content actually exists on disk (for cache lookup)
	size, exists := m.store.checkContent(d)
	if !exists {
		return ccontent.Info{}, errdefs.ErrNotFound
	}

	m.labels.l.RLock()
	labels := m.labels.labels[d]
	m.labels.l.RUnlock()

	return ccontent.Info{
		Digest: d,
		Size:   size,
		Labels: labels,
	}, nil
}

// ReaderAt returns a reader for a descriptor
func (m *ContentStore) ReaderAt(ctx context.Context, desc ocispec.Descriptor) (ccontent.ReaderAt, error) {
	// Get the file path for this descriptor
	cPath := m.store.contentPath(desc)

	// Open the file for reading
	f, err := m.store.openFile(cPath)
	if err != nil {
		return nil, errdefs.ErrNotFound
	}

	return sizeReaderAt{
		readAtCloser: f,
		size:         desc.Size,
	}, nil
}

// Writer returns a content writer given the specific options
func (m *ContentStore) Writer(ctx context.Context, opts ...ccontent.WriterOpt) (ccontent.Writer, error) {
	// this function is the original `Writer` implementation from oras 0.9.x, copied as-is
	// given that oras-go v1.2.x has changed the signature and the implementation under a "Pusher" method
	var wOpts ccontent.WriterOpts
	for _, opt := range opts {
		if err := opt(&wOpts); err != nil {
			return nil, err
		}
	}
	desc := wOpts.Desc

	// Check if content already exists in cache (by digest)
	if desc.Digest != "" {
		if size, exists := m.store.checkContent(desc.Digest); exists {
			// Content already exists, return a no-op writer that immediately succeeds
			name, _ := resolveName(desc)
			now := time.Now()
			return &noopWriter{
				desc: desc,
				status: ccontent.Status{
					Ref:       name,
					Total:     size,
					Offset:    size,
					StartedAt: now,
					UpdatedAt: now,
				},
			}, nil
		}
	}

	name, _ := resolveName(desc)

	// Create a temp file for writing
	tempFile, err := m.store.createTempFile()
	if err != nil {
		return nil, err
	}

	now := time.Now()
	return &diskWriter{
		store:    m.store,
		file:     tempFile,
		tempPath: tempFile.Name(),
		desc:     desc,
		digester: digest.Canonical.Digester(),
		status: ccontent.Status{
			Ref:       name,
			Total:     desc.Size,
			StartedAt: now,
			UpdatedAt: now,
		},
	}, nil
}

// Get returns the content for a specific descriptor
func (m *ContentStore) Get(desc ocispec.Descriptor) (ocispec.Descriptor, []byte, bool) {
	rc, err := m.store.Fetch(context.Background(), desc)
	if err != nil {
		return desc, nil, false
	}
	defer rc.Close()

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, rc); err != nil {
		return desc, nil, false
	}
	return desc, buf.Bytes(), true
}

// Set sets the content for a specific descriptor
func (m *ContentStore) Set(desc ocispec.Descriptor, content []byte) {
	if name, ok := resolveName(desc); ok {
		m.nameMap[name] = desc
	}
	_ = m.store.Push(context.Background(), desc, bytes.NewReader(content))
}

// GetByName retrieves a descriptor based on the associated name
func (m *ContentStore) GetByName(name string) (desc ocispec.Descriptor, content []byte, found bool) {
	desc, found = m.nameMap[name]
	if !found {
		return desc, nil, false
	}
	return m.Get(desc)
}

// Abort is not implemented or needed in this context
func (m *ContentStore) Abort(ctx context.Context, ref string) error {
	return nil
}

// ListStatuses is not implemented or needed in this context
func (m *ContentStore) ListStatuses(ctx context.Context, filters ...string) ([]ccontent.Status, error) {
	return []ccontent.Status{}, nil
}

// Status is not implemented or needed in this context
func (m *ContentStore) Status(ctx context.Context, ref string) (ccontent.Status, error) {
	return ccontent.Status{}, nil
}

// the rest of this file contains the original "diskWriter" implementation
// from oras 0.9.x to support the `Writer` function above as well as the
// `ReaderAt` implementation that uses the interfaces below

type readAtCloser interface {
	io.ReaderAt
	io.Closer
}

type sizeReaderAt struct {
	readAtCloser
	size int64
}

func (ra sizeReaderAt) Size() int64 {
	return ra.size
}

type nopCloser struct {
	io.ReaderAt
}

func (nopCloser) Close() error {
	return nil
}

// noopWriter is a writer that does nothing because content already exists
type noopWriter struct {
	desc   ocispec.Descriptor
	status ccontent.Status
}

func (w *noopWriter) Status() (ccontent.Status, error) {
	return w.status, nil
}

func (w *noopWriter) Digest() digest.Digest {
	return w.desc.Digest
}

func (w *noopWriter) Write(p []byte) (n int, err error) {
	// Content already exists, writes are no-op
	return len(p), nil
}

func (w *noopWriter) Commit(ctx context.Context, size int64, expected digest.Digest, opts ...ccontent.Opt) error {
	// Content already exists, commit is no-op
	return nil
}

func (w *noopWriter) Close() error {
	return nil
}

func (w *noopWriter) Truncate(size int64) error {
	if size != 0 {
		return errdefs.ErrInvalidArgument
	}
	return nil
}

type diskWriter struct {
	store    *LocalStore
	file     *os.File
	tempPath string
	desc     ocispec.Descriptor
	digester digest.Digester
	status   ccontent.Status
}

func (w *diskWriter) Status() (ccontent.Status, error) {
	return w.status, nil
}

// Digest returns the current digest of the content, up to the current write.
//
// Cannot be called concurrently with `Write`.
func (w *diskWriter) Digest() digest.Digest {
	return w.digester.Digest()
}

// Write p to the transaction.
func (w *diskWriter) Write(p []byte) (n int, err error) {
	if w.file == nil {
		return 0, errors.Wrap(errdefs.ErrFailedPrecondition, "writer closed")
	}
	n, err = w.file.Write(p)
	if err != nil {
		return n, err
	}
	w.digester.Hash().Write(p[:n])
	w.status.Offset += int64(n)
	w.status.UpdatedAt = time.Now()
	return n, nil
}

func (w *diskWriter) Commit(ctx context.Context, size int64, expected digest.Digest, opts ...ccontent.Opt) error {
	var base ccontent.Info
	for _, opt := range opts {
		if err := opt(&base); err != nil {
			return err
		}
	}

	if w.file == nil {
		return errors.Wrap(errdefs.ErrFailedPrecondition, "cannot commit on closed writer")
	}

	if size > 0 && size != w.status.Offset {
		w.file.Close()
		os.Remove(w.tempPath)
		w.file = nil
		return errors.Wrapf(errdefs.ErrFailedPrecondition, "unexpected commit size %d, expected %d", w.status.Offset, size)
	}
	if dgst := w.digester.Digest(); expected != "" && expected != dgst {
		w.file.Close()
		os.Remove(w.tempPath)
		w.file = nil
		return errors.Wrapf(errdefs.ErrFailedPrecondition, "unexpected commit digest %s, expected %s", dgst, expected)
	}

	// Close the temp file
	if err := w.file.Close(); err != nil {
		os.Remove(w.tempPath)
		w.file = nil
		return err
	}
	w.file = nil

	// Move temp file to final location
	finalPath := w.store.contentPath(w.desc)
	if err := os.Rename(w.tempPath, finalPath); err != nil {
		os.Remove(w.tempPath)
		return err
	}

	return nil
}

func (w *diskWriter) Close() error {
	if w.file != nil {
		w.file.Close()
		os.Remove(w.tempPath)
		w.file = nil
	}
	return nil
}

func (w *diskWriter) Truncate(size int64) error {
	if size != 0 {
		return errdefs.ErrInvalidArgument
	}
	if w.file == nil {
		return errors.Wrap(errdefs.ErrFailedPrecondition, "writer closed")
	}
	w.status.Offset = 0
	w.digester.Hash().Reset()
	if err := w.file.Truncate(0); err != nil {
		return err
	}
	_, err := w.file.Seek(0, 0)
	return err
}

func resolveName(desc ocispec.Descriptor) (string, bool) {
	if desc.Annotations == nil {
		return "", false
	}
	name, ok := desc.Annotations[ocispec.AnnotationRefName]
	return name, ok
}
