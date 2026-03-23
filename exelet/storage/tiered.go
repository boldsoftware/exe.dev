package storage

import (
	"context"
	"errors"
	"fmt"
	"io"

	api "exe.dev/pkg/api/exe/storage/v1"
)

// TieredStorageManager wraps multiple StorageManagers as named pools.
// It implements StorageManager by delegating to the primary pool,
// making it a drop-in replacement for single-pool configurations.
type TieredStorageManager struct {
	primary      StorageManager
	pools        map[string]StorageManager    // pool name -> StorageManager
	poolNames    []string                     // ordered list (primary first)
	backupPool   string                       // pool name to use as last resort in resolution
	poolMetadata map[string]map[string]string // pool name -> operator-defined metadata
}

// NewTieredStorageManager creates a TieredStorageManager with a primary pool
// and optional additional tiers. The primaryName is the name of the primary pool.
func NewTieredStorageManager(primaryName string, primary StorageManager, tiers map[string]StorageManager) *TieredStorageManager {
	pools := make(map[string]StorageManager, len(tiers)+1)
	pools[primaryName] = primary

	poolNames := make([]string, 0, len(tiers)+1)
	poolNames = append(poolNames, primaryName)

	for name, sm := range tiers {
		pools[name] = sm
		poolNames = append(poolNames, name)
	}

	return &TieredStorageManager{
		primary:   primary,
		pools:     pools,
		poolNames: poolNames,
	}
}

// SetBackupPool marks a pool as the backup tier, causing PoolForInstance
// to resolve it only as a last resort after all other pools are checked.
func (t *TieredStorageManager) SetBackupPool(name string) {
	t.backupPool = name
}

// SetPoolMetadata attaches operator-defined metadata to a named pool.
func (t *TieredStorageManager) SetPoolMetadata(name string, metadata map[string]string) {
	if len(metadata) == 0 {
		return
	}
	if t.poolMetadata == nil {
		t.poolMetadata = make(map[string]map[string]string)
	}
	t.poolMetadata[name] = metadata
}

// PoolMetadata returns the operator-defined metadata for a named pool.
func (t *TieredStorageManager) PoolMetadata(name string) map[string]string {
	if t.poolMetadata == nil {
		return nil
	}
	return t.poolMetadata[name]
}

// Primary returns the primary pool's StorageManager.
func (t *TieredStorageManager) Primary() StorageManager {
	return t.primary
}

// Pool returns the StorageManager for a named pool.
func (t *TieredStorageManager) Pool(name string) (StorageManager, error) {
	sm, ok := t.pools[name]
	if !ok {
		return nil, fmt.Errorf("unknown storage pool %q", name)
	}
	return sm, nil
}

// PoolNames returns all pool names in order (primary first).
func (t *TieredStorageManager) PoolNames() []string {
	return t.poolNames
}

// PoolForInstance finds which pool holds an instance by trying Get() on each pool.
// Returns the pool name, its StorageManager, and any error.
// If a backup pool is configured, it is checked last so that other durable
// storage tiers are preferred over the backup copy.
//
// Only ErrNotFound is treated as "try next pool". Any other Get() error
// (e.g. transient ZFS failures) is returned immediately so callers fail
// closed rather than silently falling through to the wrong pool.
func (t *TieredStorageManager) PoolForInstance(ctx context.Context, id string) (string, StorageManager, error) {
	var foundName string
	var foundSM StorageManager
	for _, name := range t.poolNames {
		if name == t.backupPool {
			continue // defer backup pool to last resort
		}
		sm := t.pools[name]
		_, err := sm.Get(ctx, id)
		if err == nil {
			if foundSM != nil {
				return "", nil, fmt.Errorf("instance %s found on multiple pools (%s and %s): possible split-brain, manual intervention required", id, foundName, name)
			}
			foundName = name
			foundSM = sm
			continue
		}
		if !errors.Is(err, api.ErrNotFound) {
			return "", nil, fmt.Errorf("pool %s: error checking instance %s: %w", name, id, err)
		}
	}
	if foundSM != nil {
		return foundName, foundSM, nil
	}
	// Fall back to backup pool if configured and instance exists there.
	if t.backupPool != "" {
		if sm, ok := t.pools[t.backupPool]; ok {
			_, err := sm.Get(ctx, id)
			if err == nil {
				return t.backupPool, sm, nil
			}
			if !errors.Is(err, api.ErrNotFound) {
				return "", nil, fmt.Errorf("pool %s: error checking instance %s: %w", t.backupPool, id, err)
			}
		}
	}
	return "", nil, fmt.Errorf("instance %s not found on any storage pool: %w", id, api.ErrNotFound)
}

// PoolName returns the pool name for a given StorageManager, or empty string if not found.
func (t *TieredStorageManager) PoolName(sm StorageManager) string {
	for name, pool := range t.pools {
		if pool == sm {
			return name
		}
	}
	return ""
}

// StorageManager interface — all methods delegate to the primary pool.

func (t *TieredStorageManager) Type() string {
	return t.primary.Type()
}

func (t *TieredStorageManager) Get(ctx context.Context, id string) (*api.Filesystem, error) {
	return t.primary.Get(ctx, id)
}

// GetAnyPool scans all pools for a dataset by ID. Use this for VM lookups
// where the dataset may have been migrated to a non-primary tier.
// For base image checks or new instance creation, use Get() (primary-only).
func (t *TieredStorageManager) GetAnyPool(ctx context.Context, id string) (*api.Filesystem, error) {
	for _, name := range t.poolNames {
		sm := t.pools[name]
		fs, err := sm.Get(ctx, id)
		if err == nil {
			return fs, nil
		}
		if !errors.Is(err, api.ErrNotFound) {
			return nil, fmt.Errorf("pool %s: error checking instance %s: %w", name, id, err)
		}
	}
	return nil, api.ErrNotFound
}

func (t *TieredStorageManager) Create(ctx context.Context, id string, cfg *api.FilesystemConfig) (*api.Filesystem, error) {
	return t.primary.Create(ctx, id, cfg)
}

func (t *TieredStorageManager) Clone(ctx context.Context, srcID, destID string) error {
	return t.primary.Clone(ctx, srcID, destID)
}

func (t *TieredStorageManager) Expand(ctx context.Context, id string, size uint64, resizeFilesystem bool) error {
	return t.primary.Expand(ctx, id, size, resizeFilesystem)
}

func (t *TieredStorageManager) Shrink(ctx context.Context, id string) error {
	return t.primary.Shrink(ctx, id)
}

func (t *TieredStorageManager) Load(ctx context.Context, id string) (*api.Filesystem, error) {
	return t.primary.Load(ctx, id)
}

func (t *TieredStorageManager) Mount(ctx context.Context, id string) (*api.FilesystemMountConfig, error) {
	return t.primary.Mount(ctx, id)
}

func (t *TieredStorageManager) Unmount(ctx context.Context, id string) error {
	return t.primary.Unmount(ctx, id)
}

func (t *TieredStorageManager) Rename(ctx context.Context, oldID, newID string) error {
	return t.primary.Rename(ctx, oldID, newID)
}

func (t *TieredStorageManager) Fsck(ctx context.Context, id string) error {
	return t.primary.Fsck(ctx, id)
}

func (t *TieredStorageManager) Delete(ctx context.Context, id string) error {
	// Find the pool that holds the dataset, then delete from there.
	_, sm, err := t.PoolForInstance(ctx, id)
	if err != nil {
		// Not found on any pool — delegate to primary for a consistent error.
		return t.primary.Delete(ctx, id)
	}
	return sm.Delete(ctx, id)
}

func (t *TieredStorageManager) GetDatasetName(id string) string {
	return t.primary.GetDatasetName(id)
}

func (t *TieredStorageManager) GetOrigin(id string) string {
	return t.primary.GetOrigin(id)
}

func (t *TieredStorageManager) CreateMigrationSnapshot(ctx context.Context, id string) (string, func(), error) {
	return t.primary.CreateMigrationSnapshot(ctx, id)
}

func (t *TieredStorageManager) SendSnapshot(ctx context.Context, snapName string, incremental bool, baseSnap string) (io.ReadCloser, error) {
	return t.primary.SendSnapshot(ctx, snapName, incremental, baseSnap)
}

func (t *TieredStorageManager) ReceiveSnapshot(ctx context.Context, id string, reader io.Reader) error {
	return t.primary.ReceiveSnapshot(ctx, id, reader)
}

func (t *TieredStorageManager) GetEncryptionKey(id string) ([]byte, error) {
	return t.primary.GetEncryptionKey(id)
}

func (t *TieredStorageManager) SetEncryptionKey(id string, key []byte) error {
	return t.primary.SetEncryptionKey(id, key)
}

func (t *TieredStorageManager) SnapshotExists(snapName string) bool {
	return t.primary.SnapshotExists(snapName)
}

func (t *TieredStorageManager) CreateSnapshot(ctx context.Context, snapName string) error {
	return t.primary.CreateSnapshot(ctx, snapName)
}

func (t *TieredStorageManager) DestroySnapshot(ctx context.Context, snapName string) error {
	return t.primary.DestroySnapshot(ctx, snapName)
}

func (t *TieredStorageManager) PruneOrphanedBaseImages(ctx context.Context) (int, error) {
	return t.primary.PruneOrphanedBaseImages(ctx)
}

func (t *TieredStorageManager) ListDatasets(ctx context.Context) ([]string, error) {
	return t.primary.ListDatasets(ctx)
}

func (t *TieredStorageManager) SetUserProperty(ctx context.Context, id, property, value string) error {
	return t.primary.SetUserProperty(ctx, id, property, value)
}

func (t *TieredStorageManager) GetUserProperty(ctx context.Context, id, property string) (string, error) {
	return t.primary.GetUserProperty(ctx, id, property)
}
