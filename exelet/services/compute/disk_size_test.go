package compute

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"exe.dev/exelet/services"
	storage "exe.dev/exelet/storage"
	computeapi "exe.dev/pkg/api/exe/compute/v1"
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

func mkInst(id, name string, state computeapi.VMState, ipCIDR string) *computeapi.Instance {
	return &computeapi.Instance{
		ID:    id,
		Name:  name,
		State: state,
		VMConfig: &computeapi.VMConfig{
			NetworkInterface: &computeapi.NetworkInterface{
				IP: &computeapi.IPAddress{IPV4: ipCIDR},
			},
		},
	}
}

func TestGetInstanceByIPFindsRunningInstance(t *testing.T) {
	t.Parallel()
	svc, _ := newTestService(t)

	// Several instances persisted, one of which matches. The test harness
	// has no real cloud-hypervisor socket and no StorageManager wired up;
	// if GetInstanceByIP were still going through the live-overlay path
	// (vmm.State + zfs volsize), RUNNING would get clobbered to STOPPED
	// or the storage call would error, and the lookup would fail.
	for _, inst := range []*computeapi.Instance{
		mkInst("inst-stopped", "oldbox", computeapi.VMState_STOPPED, "10.42.0.5/16"),
		mkInst("inst-other", "otherbox", computeapi.VMState_RUNNING, "10.42.0.6/16"),
		mkInst("inst-hot", "hotbox", computeapi.VMState_RUNNING, "10.42.0.7/16"),
	} {
		if err := svc.saveInstanceConfig(inst); err != nil {
			t.Fatalf("saveInstanceConfig %s: %v", inst.ID, err)
		}
	}

	id, name, vmIP, err := svc.GetInstanceByIP(t.Context(), "10.42.0.7")
	if err != nil {
		t.Fatalf("GetInstanceByIP: %v", err)
	}
	if id != "inst-hot" || name != "hotbox" || vmIP != "10.42.0.7" {
		t.Fatalf("unexpected lookup result: id=%q name=%q vmIP=%q", id, name, vmIP)
	}
}

func TestGetInstanceByIPSkipsStoppedInstances(t *testing.T) {
	t.Parallel()
	svc, _ := newTestService(t)

	// A STOPPED instance with the same IP must not be returned: stale
	// config files can linger briefly during deletion, and the IPAM
	// lease may already belong to a new VM.
	if err := svc.saveInstanceConfig(
		mkInst("inst-old", "oldbox", computeapi.VMState_STOPPED, "10.42.0.7/16"),
	); err != nil {
		t.Fatalf("saveInstanceConfig: %v", err)
	}

	_, _, _, err := svc.GetInstanceByIP(t.Context(), "10.42.0.7")
	if err == nil {
		t.Fatal("GetInstanceByIP unexpectedly succeeded for STOPPED instance")
	}
}

func TestGetInstanceByIPPartialLoadFallsThrough(t *testing.T) {
	t.Parallel()
	svc, _ := newTestService(t)

	// Healthy instance + a corrupted config.json. The lookup must still
	// succeed for the healthy one; one bad config shouldn't take down
	// the metadata service for every other VM on the host.
	if err := svc.saveInstanceConfig(
		mkInst("inst-good", "goodbox", computeapi.VMState_RUNNING, "10.42.0.7/16"),
	); err != nil {
		t.Fatalf("saveInstanceConfig: %v", err)
	}
	badDir := svc.getInstanceDir("inst-bad")
	if err := os.MkdirAll(badDir, 0o700); err != nil {
		t.Fatalf("mkdir bad: %v", err)
	}
	if err := os.WriteFile(filepath.Join(badDir, "config.json"), []byte("not protobuf"), 0o660); err != nil {
		t.Fatalf("write bad: %v", err)
	}

	id, _, _, err := svc.GetInstanceByIP(t.Context(), "10.42.0.7")
	if err != nil || id != "inst-good" {
		t.Fatalf("GetInstanceByIP = (%q, %v), want (\"inst-good\", nil)", id, err)
	}
}
