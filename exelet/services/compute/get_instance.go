package compute

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/grpc-ecosystem/go-grpc-middleware/v2/interceptors/logging"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	api "exe.dev/pkg/api/exe/compute/v1"
	storageapi "exe.dev/pkg/api/exe/storage/v1"
)

func (s *Service) GetInstance(ctx context.Context, req *api.GetInstanceRequest) (*api.GetInstanceResponse, error) {
	logging.AddFields(ctx, logging.Fields{"container_id", req.ID})

	i, err := s.getInstance(ctx, req.ID)
	if errors.Is(err, api.ErrNotFound) {
		return nil, status.Error(codes.NotFound, err.Error())
	}
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	if i != nil && i.Name != "" {
		logging.AddFields(ctx, logging.Fields{"vm_name", i.Name})
	}

	return &api.GetInstanceResponse{
		Instance: i,
	}, nil
}

func (s *Service) getInstance(ctx context.Context, id string) (*api.Instance, error) {
	return s.getInstanceWithDiskSize(ctx, id, nil, false)
}

// getInstanceWithDiskSize is like getInstance but allows callers (e.g.
// listInstances) to pass a precomputed disk-size map fetched in one
// shot. If bulkAttempted is false, this falls back to the per-VM
// readDiskSizeBytes path. If bulkAttempted is true, the persisted
// VMConfig.Disk is left untouched for instances missing from
// diskSizes — we deliberately avoid re-entering the per-VM fork path
// (the whole point of the bulk lookup) and accept showing the last
// persisted size on transient bulk-fetch failures.
func (s *Service) getInstanceWithDiskSize(ctx context.Context, id string, diskSizes map[string]*storageapi.Filesystem, bulkAttempted bool) (*api.Instance, error) {
	configPath := s.getInstanceConfigPath(id)
	data, err := os.ReadFile(configPath)
	if os.IsNotExist(err) {
		return nil, fmt.Errorf("%w: instance %s", api.ErrNotFound, id)
	}
	if err != nil {
		return nil, fmt.Errorf("reading instance config %s: %w", id, err)
	}
	i := &api.Instance{}
	if err := i.Unmarshal(data); err != nil {
		return nil, err
	}

	// If instance is in CREATING state, return as-is without querying VMM
	// (the VM doesn't exist yet during creation)
	if i.State == api.VMState_CREATING {
		return i, nil
	}

	// check state from VMM
	state, err := s.vmm.State(ctx, id)
	if err != nil {
		return nil, err
	}

	i.State = state
	if i.VMConfig != nil {
		if fs, ok := diskSizes[id]; ok {
			// Bulk-fetched volsize: source of truth. Overrides persisted
			// VMConfig.Disk so callers always see the live value.
			i.VMConfig.Disk = fs.Size
		} else if !bulkAttempted {
			if size, ok := s.readDiskSizeBytes(ctx, id); ok {
				i.VMConfig.Disk = size
			}
		}
	}

	return i, nil
}

// readDiskSizeBytes returns the current provisioned disk size from the
// storage manager (ZFS volsize). The second return value is false when the
// zvol can't be read (e.g. during creation/deletion, transient storage
// error, or tests without a StorageManager); callers should fall back to
// the persisted VMConfig.Disk in that case.
func (s *Service) readDiskSizeBytes(ctx context.Context, id string) (uint64, bool) {
	if s.context == nil || s.context.StorageManager == nil {
		return 0, false
	}
	sm, err := s.resolveStorageForInstance(ctx, id)
	if err != nil || sm == nil {
		return 0, false
	}
	fs, err := sm.Get(ctx, id)
	if err != nil {
		if !errors.Is(err, storageapi.ErrNotFound) {
			s.log.DebugContext(ctx, "read disk size from storage", "id", id, "error", err)
		}
		return 0, false
	}
	return fs.Size, true
}
