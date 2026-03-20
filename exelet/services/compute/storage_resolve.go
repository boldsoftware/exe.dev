package compute

import (
	"context"

	"exe.dev/exelet/storage"
)

// resolveStorageForInstance returns the StorageManager that holds the given instance.
// If storage tiers are configured, it scans all pools. Otherwise, it returns the primary.
func (s *Service) resolveStorageForInstance(ctx context.Context, id string) (storage.StorageManager, error) {
	tiered, ok := s.context.StorageManager.(*storage.TieredStorageManager)
	if !ok {
		return s.context.StorageManager, nil
	}
	_, sm, err := tiered.PoolForInstance(ctx, id)
	if err != nil {
		return nil, err
	}
	return sm, nil
}
