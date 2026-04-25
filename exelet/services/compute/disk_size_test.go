package compute

import (
	"context"
	"io"
	"testing"

	"exe.dev/exelet/services"
	storage "exe.dev/exelet/storage"
	storageapi "exe.dev/pkg/api/exe/storage/v1"
)

// stubStorage is a minimal storage.StorageManager that only implements Get.
type stubStorage struct {
	storage.StorageManager
	size uint64
	err  error
}

func (s *stubStorage) Type() string { return "stub" }
func (s *stubStorage) Get(_ context.Context, id string) (*storageapi.Filesystem, error) {
	if s.err != nil {
		return nil, s.err
	}
	return &storageapi.Filesystem{ID: id, Size: s.size}, nil
}

// The rest must return zero values to satisfy the interface; only Get() is
// invoked by readDiskSizeBytes.
func (s *stubStorage) Create(context.Context, string, *storageapi.FilesystemConfig) (*storageapi.Filesystem, error) {
	return nil, nil
}
func (s *stubStorage) Clone(context.Context, string, string) error                  { return nil }
func (s *stubStorage) Expand(context.Context, string, uint64, bool) error           { return nil }
func (s *stubStorage) Shrink(context.Context, string) error                         { return nil }
func (s *stubStorage) Load(context.Context, string) (*storageapi.Filesystem, error) { return nil, nil }
func (s *stubStorage) Mount(context.Context, string) (*storageapi.FilesystemMountConfig, error) {
	return nil, nil
}
func (s *stubStorage) Unmount(context.Context, string) error        { return nil }
func (s *stubStorage) Rename(context.Context, string, string) error { return nil }
func (s *stubStorage) Fsck(context.Context, string) error           { return nil }
func (s *stubStorage) Delete(context.Context, string) error         { return nil }
func (s *stubStorage) GetDatasetName(id string) string              { return id }
func (s *stubStorage) GetOrigin(string) string                      { return "" }
func (s *stubStorage) CreateMigrationSnapshot(context.Context, string) (string, func(), error) {
	return "", func() {}, nil
}

func (s *stubStorage) SendSnapshot(context.Context, string, bool, string) (io.ReadCloser, error) {
	return nil, nil
}
func (s *stubStorage) ReceiveSnapshot(context.Context, string, io.Reader) error          { return nil }
func (s *stubStorage) GetEncryptionKey(string) ([]byte, error)                           { return nil, nil }
func (s *stubStorage) SetEncryptionKey(string, []byte) error                             { return nil }
func (s *stubStorage) SnapshotExists(string) bool                                        { return false }
func (s *stubStorage) CreateSnapshot(context.Context, string) error                      { return nil }
func (s *stubStorage) DestroySnapshot(context.Context, string) error                     { return nil }
func (s *stubStorage) ReceiveSnapshotResumable(context.Context, string, io.Reader) error { return nil }
func (s *stubStorage) GetResumeToken(context.Context, string) (string, error)            { return "", nil }
func (s *stubStorage) SendSnapshotResume(context.Context, string) (io.ReadCloser, error) {
	return nil, nil
}
func (s *stubStorage) PruneOrphanedBaseImages(context.Context) (int, error)          { return 0, nil }
func (s *stubStorage) ListDatasets(context.Context) ([]string, error)                { return nil, nil }
func (s *stubStorage) SetUserProperty(context.Context, string, string, string) error { return nil }
func (s *stubStorage) GetUserProperty(context.Context, string, string) (string, error) {
	return "", nil
}

func TestReadDiskSizeBytesFromStorage(t *testing.T) {
	t.Parallel()
	svc, _ := newTestService(t)

	const want = uint64(61 * 1024 * 1024 * 1024)
	svc.context = &services.ServiceContext{
		NetworkManager: svc.context.NetworkManager,
		StorageManager: &stubStorage{size: want},
	}

	got, ok := svc.readDiskSizeBytes(t.Context(), "some-id")
	if !ok || got != want {
		t.Fatalf("readDiskSizeBytes = (%d, %v), want (%d, true)", got, ok, want)
	}
}

func TestReadDiskSizeBytesMissingStorage(t *testing.T) {
	t.Parallel()
	svc, _ := newTestService(t)
	// No StorageManager configured on the context: must not panic, must return (0, false).
	got, ok := svc.readDiskSizeBytes(t.Context(), "id")
	if ok || got != 0 {
		t.Fatalf("want (0, false) on missing storage, got (%d, %v)", got, ok)
	}
}
