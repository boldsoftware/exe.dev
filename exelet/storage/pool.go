package storage

import (
	"context"
	"net/url"
	"strings"
)

// PoolNameFromAddress extracts the ZFS pool/dataset name from a storage manager
// address URL (e.g., "zfs:///var/tmp/exelet/storage?dataset=tank" -> "tank").
func PoolNameFromAddress(addr string) string {
	u, err := url.Parse(addr)
	if err != nil {
		return ""
	}
	dataset := u.Query().Get("dataset")
	if dataset == "" {
		return ""
	}
	// Extract pool name (first component of dataset path)
	if idx := strings.IndexByte(dataset, '/'); idx >= 0 {
		return dataset[:idx]
	}
	return dataset
}

// ResolveForID returns the StorageManager that holds the given dataset ID.
// If sm is a TieredStorageManager, it scans all pools. Otherwise, returns sm directly.
func ResolveForID(ctx context.Context, sm StorageManager, id string) StorageManager {
	tiered, ok := sm.(*TieredStorageManager)
	if !ok {
		return sm
	}
	_, resolved, err := tiered.PoolForInstance(ctx, id)
	if err != nil {
		// Fall back to primary if not found on any pool
		return tiered.Primary()
	}
	return resolved
}
