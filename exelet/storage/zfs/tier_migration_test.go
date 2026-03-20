//go:build linux

package zfs_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"exe.dev/exelet/storage"
	"exe.dev/exelet/storage/zfs"

	api "exe.dev/pkg/api/exe/storage/v1"
)

// skipIfNotRoot skips the test if not running as root (ZFS requires root).
func skipIfNotRoot(t *testing.T) {
	t.Helper()
	if os.Getuid() != 0 {
		t.Skip("skipping: ZFS tests require root")
	}
}

// skipIfNoZFS skips the test if ZFS tooling is not installed.
func skipIfNoZFS(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("zpool"); err != nil {
		t.Skip("skipping: zpool not found in PATH")
	}
	if _, err := exec.LookPath("zfs"); err != nil {
		t.Skip("skipping: zfs not found in PATH")
	}
}

// skipTierMigration applies all common skip conditions for tier migration tests.
func skipTierMigration(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping: tier migration tests are slow")
	}
	skipIfNotRoot(t)
	skipIfNoZFS(t)
}

// testPoolPrefix is used to identify test pools for cleanup.
const testPoolPrefix = "tiert-"

// createTestPool creates an ephemeral ZFS pool backed by a sparse file.
// Returns the pool name and a cleanup function that destroys the pool.
func createTestPool(t *testing.T, suffix string) string {
	t.Helper()

	// Generate a unique pool name to avoid collisions with parallel tests.
	randBytes := make([]byte, 4)
	if _, err := rand.Read(randBytes); err != nil {
		t.Fatalf("failed to generate random bytes: %v", err)
	}
	poolName := fmt.Sprintf("%s%s-%s", testPoolPrefix, suffix, hex.EncodeToString(randBytes))

	imgDir := t.TempDir()
	imgPath := filepath.Join(imgDir, poolName+".img")

	// Create sparse file (no real disk usage).
	cmd := exec.Command("truncate", "-s", "512M", imgPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("truncate failed: %v (%s)", err, out)
	}

	// Create zpool.
	cmd = exec.Command("zpool", "create", poolName, imgPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("zpool create failed: %v (%s)", err, out)
	}

	t.Cleanup(func() {
		cmd := exec.Command("zpool", "destroy", "-f", poolName)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Logf("warning: zpool destroy %s failed: %v (%s)", poolName, err, out)
		}
	})

	return poolName
}

// newTestZFS creates a ZFS StorageManager backed by the given pool name.
// The data directory is created under t.TempDir().
func newTestZFS(t *testing.T, poolName string) *zfs.ZFS {
	t.Helper()

	dataDir := filepath.Join(t.TempDir(), "data-"+poolName)
	addr := fmt.Sprintf("zfs://%s?dataset=%s", dataDir, poolName)
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))

	sm, err := zfs.NewZFS(addr, log)
	if err != nil {
		t.Fatalf("NewZFS(%s) failed: %v", addr, err)
	}
	return sm
}

const (
	testVolumeSize = 64 * 1024 * 1024 // 64 MiB
	sentinelFile   = "sentinel.txt"
	sentinelData   = "hello from pool-a"
)

func TestTierMigration_StoppedDataset(t *testing.T) {
	skipTierMigration(t)

	ctx := context.Background()

	poolA := createTestPool(t, "a")
	poolB := createTestPool(t, "b")
	smA := newTestZFS(t, poolA)
	smB := newTestZFS(t, poolB)

	const vmID = "vm-stopped"

	// 1. Create a volume on pool-a.
	fs, err := smA.Create(ctx, vmID, &api.FilesystemConfig{
		Size:   testVolumeSize,
		FsType: "ext4",
	})
	if err != nil {
		t.Fatalf("Create on pool-a: %v", err)
	}
	t.Logf("created volume on pool-a: %s", fs.Path)

	// 2. Mount, write sentinel, unmount.
	mnt, err := smA.Mount(ctx, vmID)
	if err != nil {
		t.Fatalf("Mount on pool-a: %v", err)
	}
	if err := os.WriteFile(filepath.Join(mnt.Path, sentinelFile), []byte(sentinelData), 0o644); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}
	if err := smA.Unmount(ctx, vmID); err != nil {
		t.Fatalf("Unmount on pool-a: %v", err)
	}

	// 3. Create migration snapshot.
	snapName, cleanup, err := smA.CreateMigrationSnapshot(ctx, vmID)
	if err != nil {
		t.Fatalf("CreateMigrationSnapshot: %v", err)
	}
	defer cleanup()

	// 4. Send snapshot from pool-a.
	reader, err := smA.SendSnapshot(ctx, snapName, false, "")
	if err != nil {
		t.Fatalf("SendSnapshot: %v", err)
	}

	// 5. Receive snapshot on pool-b.
	if err := smB.ReceiveSnapshot(ctx, vmID, reader); err != nil {
		t.Fatalf("ReceiveSnapshot: %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("reader.Close: %v", err)
	}

	// 6. Verify dataset exists on pool-b.
	fsB, err := smB.Get(ctx, vmID)
	if err != nil {
		t.Fatalf("Get on pool-b: %v", err)
	}
	expectedPath := fmt.Sprintf("/dev/zvol/%s/%s", poolB, vmID)
	if fsB.Path != expectedPath {
		t.Errorf("path = %s, want %s", fsB.Path, expectedPath)
	}

	// 7. Load on pool-b (ensures zvol device exists).
	if _, err := smB.Load(ctx, vmID); err != nil {
		t.Fatalf("Load on pool-b: %v", err)
	}

	// 8. Mount on pool-b and verify sentinel data.
	mntB, err := smB.Mount(ctx, vmID)
	if err != nil {
		t.Fatalf("Mount on pool-b: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(mntB.Path, sentinelFile))
	if err != nil {
		t.Fatalf("read sentinel on pool-b: %v", err)
	}
	if string(got) != sentinelData {
		t.Errorf("sentinel = %q, want %q", got, sentinelData)
	}
	if err := smB.Unmount(ctx, vmID); err != nil {
		t.Fatalf("Unmount on pool-b: %v", err)
	}

	// 9. Delete from pool-a.
	if err := smA.Delete(ctx, vmID); err != nil {
		t.Fatalf("Delete on pool-a: %v", err)
	}

	// 10. Verify gone from pool-a.
	if _, err := smA.Get(ctx, vmID); err == nil {
		t.Fatal("expected Get on pool-a to fail after delete")
	}
}

func TestTierMigration_EncryptionKeyPreserved(t *testing.T) {
	skipTierMigration(t)

	ctx := context.Background()

	poolA := createTestPool(t, "enc-a")
	poolB := createTestPool(t, "enc-b")
	smA := newTestZFS(t, poolA)
	smB := newTestZFS(t, poolB)

	const vmID = "vm-encrypted"

	// Generate a 32-byte hex encryption key (64 hex chars for aes-256-gcm).
	keyBytes := make([]byte, 32)
	if _, err := rand.Read(keyBytes); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	encKey := hex.EncodeToString(keyBytes)

	// 1. Create encrypted volume on pool-a.
	_, err := smA.Create(ctx, vmID, &api.FilesystemConfig{
		Size:          testVolumeSize,
		FsType:        "ext4",
		EncryptionKey: encKey,
	})
	if err != nil {
		t.Fatalf("Create encrypted on pool-a: %v", err)
	}

	// 2. Mount, write sentinel, unmount.
	mnt, err := smA.Mount(ctx, vmID)
	if err != nil {
		t.Fatalf("Mount on pool-a: %v", err)
	}
	if err := os.WriteFile(filepath.Join(mnt.Path, sentinelFile), []byte(sentinelData), 0o644); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}
	if err := smA.Unmount(ctx, vmID); err != nil {
		t.Fatalf("Unmount on pool-a: %v", err)
	}

	// 3. Migrate via send/recv.
	snapName, cleanup, err := smA.CreateMigrationSnapshot(ctx, vmID)
	if err != nil {
		t.Fatalf("CreateMigrationSnapshot: %v", err)
	}
	defer cleanup()

	reader, err := smA.SendSnapshot(ctx, snapName, false, "")
	if err != nil {
		t.Fatalf("SendSnapshot: %v", err)
	}

	if err := smB.ReceiveSnapshot(ctx, vmID, reader); err != nil {
		t.Fatalf("ReceiveSnapshot: %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("reader.Close: %v", err)
	}

	// 4. Transfer encryption key from pool-a to pool-b.
	key, err := smA.GetEncryptionKey(vmID)
	if err != nil {
		t.Fatalf("GetEncryptionKey from pool-a: %v", err)
	}
	if key == nil {
		t.Fatal("expected non-nil encryption key from pool-a")
	}
	if err := smB.SetEncryptionKey(vmID, key); err != nil {
		t.Fatalf("SetEncryptionKey on pool-b: %v", err)
	}

	// 5. Load on pool-b (loads encryption key and verifies device is accessible).
	if _, err := smB.Load(ctx, vmID); err != nil {
		t.Fatalf("Load on pool-b: %v", err)
	}

	// 6. Mount on pool-b, verify data integrity.
	mntB, err := smB.Mount(ctx, vmID)
	if err != nil {
		t.Fatalf("Mount on pool-b: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(mntB.Path, sentinelFile))
	if err != nil {
		t.Fatalf("read sentinel on pool-b: %v", err)
	}
	if string(got) != sentinelData {
		t.Errorf("sentinel = %q, want %q", got, sentinelData)
	}
	if err := smB.Unmount(ctx, vmID); err != nil {
		t.Fatalf("Unmount on pool-b: %v", err)
	}
}

func TestTierMigration_IncrementalSend(t *testing.T) {
	skipTierMigration(t)

	ctx := context.Background()

	poolA := createTestPool(t, "inc-a")
	poolB := createTestPool(t, "inc-b")
	smA := newTestZFS(t, poolA)
	smB := newTestZFS(t, poolB)

	const vmID = "vm-incremental"
	const phase2File = "phase2.txt"
	const phase2Data = "written between phases"

	// 1. Create volume on pool-a and write initial data.
	_, err := smA.Create(ctx, vmID, &api.FilesystemConfig{
		Size:   testVolumeSize,
		FsType: "ext4",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	mnt, err := smA.Mount(ctx, vmID)
	if err != nil {
		t.Fatalf("Mount: %v", err)
	}
	if err := os.WriteFile(filepath.Join(mnt.Path, sentinelFile), []byte(sentinelData), 0o644); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}
	if err := smA.Unmount(ctx, vmID); err != nil {
		t.Fatalf("Unmount: %v", err)
	}

	// 2. Phase 1: create @migration-pre snapshot, full send/recv.
	dsNameA := smA.GetDatasetName(vmID)
	preSnapName := dsNameA + "@migration-pre"
	if err := smA.CreateSnapshot(ctx, preSnapName); err != nil {
		t.Fatalf("CreateSnapshot(@migration-pre): %v", err)
	}
	t.Cleanup(func() { smA.DestroySnapshot(ctx, preSnapName) })

	reader, err := smA.SendSnapshot(ctx, preSnapName, false, "")
	if err != nil {
		t.Fatalf("SendSnapshot (full): %v", err)
	}
	if err := smB.ReceiveSnapshot(ctx, vmID, reader); err != nil {
		t.Fatalf("ReceiveSnapshot (full): %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("reader.Close (full): %v", err)
	}

	// 3. Write additional data on pool-a (simulates VM writes between phases).
	mnt, err = smA.Mount(ctx, vmID)
	if err != nil {
		t.Fatalf("Mount for phase2 writes: %v", err)
	}
	if err := os.WriteFile(filepath.Join(mnt.Path, phase2File), []byte(phase2Data), 0o644); err != nil {
		t.Fatalf("write phase2 data: %v", err)
	}
	if err := smA.Unmount(ctx, vmID); err != nil {
		t.Fatalf("Unmount after phase2 writes: %v", err)
	}

	// 4. Phase 2: create @migration snapshot, incremental send.
	migSnapName, migCleanup, err := smA.CreateMigrationSnapshot(ctx, vmID)
	if err != nil {
		t.Fatalf("CreateMigrationSnapshot: %v", err)
	}
	defer migCleanup()

	reader, err = smA.SendSnapshot(ctx, migSnapName, true, preSnapName)
	if err != nil {
		t.Fatalf("SendSnapshot (incremental): %v", err)
	}
	if err := smB.ReceiveSnapshot(ctx, vmID, reader); err != nil {
		t.Fatalf("ReceiveSnapshot (incremental): %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("reader.Close (incremental): %v", err)
	}

	// 5. Verify both original and additional data on pool-b.
	if _, err := smB.Load(ctx, vmID); err != nil {
		t.Fatalf("Load on pool-b: %v", err)
	}
	mntB, err := smB.Mount(ctx, vmID)
	if err != nil {
		t.Fatalf("Mount on pool-b: %v", err)
	}

	// Check original data.
	got, err := os.ReadFile(filepath.Join(mntB.Path, sentinelFile))
	if err != nil {
		t.Fatalf("read sentinel on pool-b: %v", err)
	}
	if string(got) != sentinelData {
		t.Errorf("sentinel = %q, want %q", got, sentinelData)
	}

	// Check phase 2 data.
	got2, err := os.ReadFile(filepath.Join(mntB.Path, phase2File))
	if err != nil {
		t.Fatalf("read phase2 on pool-b: %v", err)
	}
	if string(got2) != phase2Data {
		t.Errorf("phase2 = %q, want %q", got2, phase2Data)
	}

	if err := smB.Unmount(ctx, vmID); err != nil {
		t.Fatalf("Unmount on pool-b: %v", err)
	}

	// Cleanup snapshots on pool-b.
	dsNameB := smB.GetDatasetName(vmID)
	smB.DestroySnapshot(ctx, dsNameB+"@migration")
	smB.DestroySnapshot(ctx, dsNameB+"@migration-pre")
}

func TestTierMigration_PoolForInstance(t *testing.T) {
	skipTierMigration(t)

	ctx := context.Background()

	poolA := createTestPool(t, "pfi-a")
	poolB := createTestPool(t, "pfi-b")
	smA := newTestZFS(t, poolA)
	smB := newTestZFS(t, poolB)

	const vmID = "vm-poolfor"

	// Create TieredStorageManager with pool-a as primary.
	tiered := storage.NewTieredStorageManager(poolA, smA, map[string]storage.StorageManager{
		poolB: smB,
	})

	// 1. Create volume on pool-a.
	if _, err := smA.Create(ctx, vmID, &api.FilesystemConfig{
		Size:   testVolumeSize,
		FsType: "ext4",
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// 2. PoolForInstance should return pool-a.
	poolName, _, err := tiered.PoolForInstance(ctx, vmID)
	if err != nil {
		t.Fatalf("PoolForInstance (before migration): %v", err)
	}
	if poolName != poolA {
		t.Errorf("PoolForInstance = %q, want %q", poolName, poolA)
	}

	// 3. Migrate to pool-b.
	snapName, cleanup, err := smA.CreateMigrationSnapshot(ctx, vmID)
	if err != nil {
		t.Fatalf("CreateMigrationSnapshot: %v", err)
	}
	defer cleanup()

	reader, err := smA.SendSnapshot(ctx, snapName, false, "")
	if err != nil {
		t.Fatalf("SendSnapshot: %v", err)
	}
	if err := smB.ReceiveSnapshot(ctx, vmID, reader); err != nil {
		t.Fatalf("ReceiveSnapshot: %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("reader.Close: %v", err)
	}

	// 4. Delete from pool-a.
	if err := smA.Delete(ctx, vmID); err != nil {
		t.Fatalf("Delete from pool-a: %v", err)
	}

	// 5. PoolForInstance should now return pool-b.
	poolName, _, err = tiered.PoolForInstance(ctx, vmID)
	if err != nil {
		t.Fatalf("PoolForInstance (after migration): %v", err)
	}
	if poolName != poolB {
		t.Errorf("PoolForInstance = %q, want %q", poolName, poolB)
	}
}

func TestTierMigration_ListDatasets(t *testing.T) {
	skipTierMigration(t)

	ctx := context.Background()

	poolA := createTestPool(t, "ld-a")
	poolB := createTestPool(t, "ld-b")
	smA := newTestZFS(t, poolA)
	smB := newTestZFS(t, poolB)

	// Create volumes on each pool.
	const vmA = "vm-list-a"
	const vmB = "vm-list-b"

	if _, err := smA.Create(ctx, vmA, &api.FilesystemConfig{
		Size:   testVolumeSize,
		FsType: "ext4",
	}); err != nil {
		t.Fatalf("Create vm-a: %v", err)
	}

	if _, err := smB.Create(ctx, vmB, &api.FilesystemConfig{
		Size:   testVolumeSize,
		FsType: "ext4",
	}); err != nil {
		t.Fatalf("Create vm-b: %v", err)
	}

	// Verify each pool lists only its own volume.
	dsA, err := smA.ListDatasets(ctx)
	if err != nil {
		t.Fatalf("ListDatasets pool-a: %v", err)
	}
	if !contains(dsA, vmA) {
		t.Errorf("pool-a datasets %v should contain %q", dsA, vmA)
	}
	if contains(dsA, vmB) {
		t.Errorf("pool-a datasets %v should not contain %q", dsA, vmB)
	}

	dsB, err := smB.ListDatasets(ctx)
	if err != nil {
		t.Fatalf("ListDatasets pool-b: %v", err)
	}
	if !contains(dsB, vmB) {
		t.Errorf("pool-b datasets %v should contain %q", dsB, vmB)
	}
	if contains(dsB, vmA) {
		t.Errorf("pool-b datasets %v should not contain %q", dsB, vmA)
	}

	// Verify TieredStorageManager iteration covers both pools.
	tiered := storage.NewTieredStorageManager(poolA, smA, map[string]storage.StorageManager{
		poolB: smB,
	})

	var allIDs []string
	for _, name := range tiered.PoolNames() {
		sm, err := tiered.Pool(name)
		if err != nil {
			t.Fatalf("Pool(%s): %v", name, err)
		}
		ids, err := sm.ListDatasets(ctx)
		if err != nil {
			t.Fatalf("ListDatasets(%s): %v", name, err)
		}
		allIDs = append(allIDs, ids...)
	}

	if !contains(allIDs, vmA) || !contains(allIDs, vmB) {
		t.Errorf("combined datasets %v should contain both %q and %q", allIDs, vmA, vmB)
	}
}

func contains(ss []string, target string) bool {
	for _, s := range ss {
		if s == target {
			return true
		}
	}
	return false
}
