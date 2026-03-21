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

// MetadataFromAddress parses query parameters from a storage tier URL and
// returns all non-reserved params as metadata. The "dataset" param is reserved.
func MetadataFromAddress(addr string) map[string]string {
	u, err := url.Parse(addr)
	if err != nil {
		return nil
	}
	md := make(map[string]string)
	for key, vals := range u.Query() {
		if key == "dataset" {
			continue
		}
		if len(vals) > 0 {
			md[key] = vals[0]
		}
	}
	if len(md) == 0 {
		return nil
	}
	return md
}

// ResolveForID returns the StorageManager that holds the given dataset ID.
// If sm is a TieredStorageManager, it scans all pools. Otherwise, returns sm directly.
// Returns an error if pool resolution fails (transient backend error or not found).
func ResolveForID(ctx context.Context, sm StorageManager, id string) (StorageManager, error) {
	tiered, ok := sm.(*TieredStorageManager)
	if !ok {
		return sm, nil
	}
	_, resolved, err := tiered.PoolForInstance(ctx, id)
	if err != nil {
		return nil, err
	}
	return resolved, nil
}
