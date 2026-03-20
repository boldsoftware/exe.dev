package storage

import (
	"context"
	"io"
	"testing"

	api "exe.dev/pkg/api/exe/storage/v1"
)

// mockStorageManager is a minimal StorageManager for testing TieredStorageManager.
type mockStorageManager struct {
	name     string
	datasets map[string]*api.Filesystem // id -> Filesystem
}

func newMockSM(name string, instanceIDs ...string) *mockStorageManager {
	m := &mockStorageManager{
		name:     name,
		datasets: make(map[string]*api.Filesystem),
	}
	for _, id := range instanceIDs {
		m.datasets[id] = &api.Filesystem{
			Path: "/dev/zvol/" + name + "/" + id,
		}
	}
	return m
}

func (m *mockStorageManager) Type() string { return "mock" }
func (m *mockStorageManager) Get(_ context.Context, id string) (*api.Filesystem, error) {
	fs, ok := m.datasets[id]
	if !ok {
		return nil, api.ErrNotFound
	}
	return fs, nil
}
func (m *mockStorageManager) Create(_ context.Context, _ string, _ *api.FilesystemConfig) (*api.Filesystem, error) {
	return nil, nil
}
func (m *mockStorageManager) Clone(_ context.Context, _, _ string) error       { return nil }
func (m *mockStorageManager) Expand(_ context.Context, _ string, _ uint64, _ bool) error {
	return nil
}
func (m *mockStorageManager) Shrink(_ context.Context, _ string) error { return nil }
func (m *mockStorageManager) Load(_ context.Context, id string) (*api.Filesystem, error) {
	return m.Get(context.Background(), id)
}
func (m *mockStorageManager) Mount(_ context.Context, _ string) (*api.FilesystemMountConfig, error) {
	return nil, nil
}
func (m *mockStorageManager) Unmount(_ context.Context, _ string) error        { return nil }
func (m *mockStorageManager) Rename(_ context.Context, _, _ string) error      { return nil }
func (m *mockStorageManager) Fsck(_ context.Context, _ string) error           { return nil }
func (m *mockStorageManager) Delete(_ context.Context, id string) error {
	delete(m.datasets, id)
	return nil
}
func (m *mockStorageManager) GetDatasetName(id string) string                  { return m.name + "/" + id }
func (m *mockStorageManager) GetOrigin(_ string) string                        { return "" }
func (m *mockStorageManager) CreateMigrationSnapshot(_ context.Context, _ string) (string, func(), error) {
	return "", func() {}, nil
}
func (m *mockStorageManager) SendSnapshot(_ context.Context, _ string, _ bool, _ string) (io.ReadCloser, error) {
	return nil, nil
}
func (m *mockStorageManager) ReceiveSnapshot(_ context.Context, _ string, _ io.Reader) error {
	return nil
}
func (m *mockStorageManager) GetEncryptionKey(_ string) ([]byte, error)        { return nil, nil }
func (m *mockStorageManager) SetEncryptionKey(_ string, _ []byte) error        { return nil }
func (m *mockStorageManager) SnapshotExists(_ string) bool                     { return false }
func (m *mockStorageManager) CreateSnapshot(_ context.Context, _ string) error { return nil }
func (m *mockStorageManager) DestroySnapshot(_ context.Context, _ string) error {
	return nil
}
func (m *mockStorageManager) PruneOrphanedBaseImages(_ context.Context) (int, error) { return 0, nil }
func (m *mockStorageManager) ListDatasets(_ context.Context) ([]string, error) {
	ids := make([]string, 0, len(m.datasets))
	for id := range m.datasets {
		ids = append(ids, id)
	}
	return ids, nil
}
func (m *mockStorageManager) SetUserProperty(_ context.Context, _, _, _ string) error { return nil }
func (m *mockStorageManager) GetUserProperty(_ context.Context, _, _ string) (string, error) {
	return "", nil
}

func TestTieredStorageManager_DelegatesToPrimary(t *testing.T) {
	primary := newMockSM("tank", "vm-1")
	tiered := NewTieredStorageManager("tank", primary, nil)

	// Type delegates to primary
	if got := tiered.Type(); got != "mock" {
		t.Errorf("Type() = %q, want %q", got, "mock")
	}

	// Get finds instance on primary
	fs, err := tiered.Get(context.Background(), "vm-1")
	if err != nil {
		t.Fatalf("Get() error: %v", err)
	}
	if fs.Path != "/dev/zvol/tank/vm-1" {
		t.Errorf("Get() path = %q, want %q", fs.Path, "/dev/zvol/tank/vm-1")
	}

	// GetDatasetName delegates to primary
	if got := tiered.GetDatasetName("vm-1"); got != "tank/vm-1" {
		t.Errorf("GetDatasetName() = %q, want %q", got, "tank/vm-1")
	}
}

func TestTieredStorageManager_PoolNames(t *testing.T) {
	primary := newMockSM("tank")
	tier1 := newMockSM("nvme")
	tier2 := newMockSM("backup")

	tiered := NewTieredStorageManager("tank", primary, map[string]StorageManager{
		"nvme":   tier1,
		"backup": tier2,
	})

	names := tiered.PoolNames()
	if names[0] != "tank" {
		t.Errorf("first pool should be primary 'tank', got %q", names[0])
	}
	if len(names) != 3 {
		t.Errorf("PoolNames() len = %d, want 3", len(names))
	}
}

func TestTieredStorageManager_Pool(t *testing.T) {
	primary := newMockSM("tank")
	nvme := newMockSM("nvme")

	tiered := NewTieredStorageManager("tank", primary, map[string]StorageManager{
		"nvme": nvme,
	})

	sm, err := tiered.Pool("nvme")
	if err != nil {
		t.Fatalf("Pool(nvme) error: %v", err)
	}
	if sm != nvme {
		t.Error("Pool(nvme) returned wrong manager")
	}

	_, err = tiered.Pool("nonexistent")
	if err == nil {
		t.Error("Pool(nonexistent) should return error")
	}
}

func TestTieredStorageManager_PoolForInstance(t *testing.T) {
	primary := newMockSM("tank", "vm-1")
	nvme := newMockSM("nvme", "vm-2")

	tiered := NewTieredStorageManager("tank", primary, map[string]StorageManager{
		"nvme": nvme,
	})

	ctx := context.Background()

	// vm-1 is on primary
	name, sm, err := tiered.PoolForInstance(ctx, "vm-1")
	if err != nil {
		t.Fatalf("PoolForInstance(vm-1) error: %v", err)
	}
	if name != "tank" {
		t.Errorf("PoolForInstance(vm-1) pool = %q, want %q", name, "tank")
	}
	if sm != primary {
		t.Error("PoolForInstance(vm-1) returned wrong manager")
	}

	// vm-2 is on nvme
	name, sm, err = tiered.PoolForInstance(ctx, "vm-2")
	if err != nil {
		t.Fatalf("PoolForInstance(vm-2) error: %v", err)
	}
	if name != "nvme" {
		t.Errorf("PoolForInstance(vm-2) pool = %q, want %q", name, "nvme")
	}
	if sm != nvme {
		t.Error("PoolForInstance(vm-2) returned wrong manager")
	}

	// vm-3 doesn't exist
	_, _, err = tiered.PoolForInstance(ctx, "vm-3")
	if err == nil {
		t.Error("PoolForInstance(vm-3) should return error")
	}
}

func TestTieredStorageManager_PoolName(t *testing.T) {
	primary := newMockSM("tank")
	nvme := newMockSM("nvme")

	tiered := NewTieredStorageManager("tank", primary, map[string]StorageManager{
		"nvme": nvme,
	})

	if got := tiered.PoolName(primary); got != "tank" {
		t.Errorf("PoolName(primary) = %q, want %q", got, "tank")
	}
	if got := tiered.PoolName(nvme); got != "nvme" {
		t.Errorf("PoolName(nvme) = %q, want %q", got, "nvme")
	}
	if got := tiered.PoolName(newMockSM("other")); got != "" {
		t.Errorf("PoolName(unknown) = %q, want empty", got)
	}
}

func TestTieredStorageManager_Primary(t *testing.T) {
	primary := newMockSM("tank")
	tiered := NewTieredStorageManager("tank", primary, nil)

	if tiered.Primary() != primary {
		t.Error("Primary() should return the primary manager")
	}
}

func TestTieredStorageManager_GetIsPrimaryOnly(t *testing.T) {
	primary := newMockSM("tank", "vm-1")
	nvme := newMockSM("nvme", "vm-2")

	tiered := NewTieredStorageManager("tank", primary, map[string]StorageManager{
		"nvme": nvme,
	})

	ctx := context.Background()

	// Get finds vm-1 on primary
	fs, err := tiered.Get(ctx, "vm-1")
	if err != nil {
		t.Fatalf("Get(vm-1) error: %v", err)
	}
	if fs.Path != "/dev/zvol/tank/vm-1" {
		t.Errorf("Get(vm-1) path = %q, want /dev/zvol/tank/vm-1", fs.Path)
	}

	// Get does NOT find vm-2 (only on nvme, not primary)
	_, err = tiered.Get(ctx, "vm-2")
	if err == nil {
		t.Error("Get(vm-2) should return error (primary-only)")
	}
}

func TestTieredStorageManager_GetAnyPoolScansAll(t *testing.T) {
	primary := newMockSM("tank", "vm-1")
	nvme := newMockSM("nvme", "vm-2")

	tiered := NewTieredStorageManager("tank", primary, map[string]StorageManager{
		"nvme": nvme,
	})

	ctx := context.Background()

	// GetAnyPool finds vm-1 on primary
	fs, err := tiered.GetAnyPool(ctx, "vm-1")
	if err != nil {
		t.Fatalf("GetAnyPool(vm-1) error: %v", err)
	}
	if fs.Path != "/dev/zvol/tank/vm-1" {
		t.Errorf("GetAnyPool(vm-1) path = %q, want /dev/zvol/tank/vm-1", fs.Path)
	}

	// GetAnyPool finds vm-2 on nvme
	fs, err = tiered.GetAnyPool(ctx, "vm-2")
	if err != nil {
		t.Fatalf("GetAnyPool(vm-2) error: %v", err)
	}
	if fs.Path != "/dev/zvol/nvme/vm-2" {
		t.Errorf("GetAnyPool(vm-2) path = %q, want /dev/zvol/nvme/vm-2", fs.Path)
	}

	// GetAnyPool returns error for non-existent
	_, err = tiered.GetAnyPool(ctx, "vm-999")
	if err == nil {
		t.Error("GetAnyPool(vm-999) should return error")
	}
}

func TestTieredStorageManager_DeleteScansAllPools(t *testing.T) {
	primary := newMockSM("tank", "vm-1")
	nvme := newMockSM("nvme", "vm-2")

	tiered := NewTieredStorageManager("tank", primary, map[string]StorageManager{
		"nvme": nvme,
	})

	ctx := context.Background()

	// Delete on nvme pool should find and delete vm-2
	if err := tiered.Delete(ctx, "vm-2"); err != nil {
		t.Fatalf("Delete(vm-2) error: %v", err)
	}

	// vm-2 should no longer be found
	_, err := tiered.Get(ctx, "vm-2")
	if err == nil {
		t.Error("Get(vm-2) should fail after delete")
	}
}

func TestTieredStorageManager_SinglePool(t *testing.T) {
	// With no tiers, should still work as a single-pool wrapper
	primary := newMockSM("tank", "vm-1")
	tiered := NewTieredStorageManager("tank", primary, nil)

	names := tiered.PoolNames()
	if len(names) != 1 || names[0] != "tank" {
		t.Errorf("single pool PoolNames() = %v, want [tank]", names)
	}

	ctx := context.Background()
	name, _, err := tiered.PoolForInstance(ctx, "vm-1")
	if err != nil {
		t.Fatalf("PoolForInstance error: %v", err)
	}
	if name != "tank" {
		t.Errorf("PoolForInstance pool = %q, want %q", name, "tank")
	}
}
