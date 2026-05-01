package compute

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	api "exe.dev/pkg/api/exe/compute/v1"
	storageapi "exe.dev/pkg/api/exe/storage/v1"
)

func (s *Service) ListInstances(req *api.ListInstancesRequest, stream api.ComputeService_ListInstancesServer) error {
	ctx := stream.Context()
	instances, err := s.listInstances(ctx)
	if err != nil {
		return status.Error(codes.Internal, err.Error())
	}

	for _, i := range instances {
		if err := stream.Send(&api.ListInstancesResponse{
			Instance: i,
		}); err != nil {
			return status.Error(codes.Internal, err.Error())
		}
	}

	return nil
}

// listInstances returns all readable instance configs, enriched with the
// live VM state from the VMM and the live disk size from the storage
// manager. If one or more configs fail to load, those are skipped and a
// non-nil error is returned alongside the partial list. Callers that
// mutate durable state based on this list (e.g. IPAM reconciliation) MUST
// treat a non-nil error as "do not act" — a partial list combined with
// destructive reconciliation caused the duplicate-IP incident that
// motivated this contract. Callers that only read the list (startup
// recovery, gRPC ListInstances) can proceed on the partial list after
// logging the error.
//
// Disk sizes are obtained with one StorageManager.GetAll call per
// invocation (which on ZFS is one `zfs list` per pool), instead of one
// `zfs get` per VM per pool. State is refreshed per-VM via the VMM
// (unix-socket call, not a fork). Callers that only need the persisted
// config (e.g. IP-to-instance lookup on the metadata hot path) should
// use listInstanceConfigs instead.
func (s *Service) listInstances(ctx context.Context) ([]*api.Instance, error) {
	configs, err := filepath.Glob(filepath.Join(s.getInstanceDir("*"), "config.json"))
	if err != nil {
		return nil, err
	}

	// Bulk-fetch disk sizes once per invocation. We deliberately do NOT
	// fall back to per-VM readDiskSizeBytes on bulk failure: that would
	// reintroduce the O(N·P) fork storm this code path was added to
	// eliminate, and a transient storage hiccup would torch the host's
	// CPU. On error we proceed with whatever partial map we got (or
	// none) and let getInstanceWithDiskSize keep the persisted
	// VMConfig.Disk for missing entries.
	var diskSizes map[string]*storageapi.Filesystem
	bulkAttempted := false
	if s.context != nil && s.context.StorageManager != nil {
		bulkAttempted = true
		diskSizes, err = s.context.StorageManager.GetAll(ctx)
		if err != nil {
			s.log.WarnContext(ctx, "listInstances: bulk storage GetAll failed; reporting persisted disk sizes for missing IDs", "error", err)
		}
	}

	instances := []*api.Instance{}
	var loadErrs []string
	for _, config := range configs {
		id := filepath.Base(filepath.Dir(config))
		i, err := s.getInstanceWithDiskSize(ctx, id, diskSizes, bulkAttempted)
		if err != nil {
			// A legitimate race: filepath.Glob saw the directory, but
			// DeleteInstance removed it before getInstance could read
			// the config. Treat this as "not present" and move on.
			if errors.Is(err, api.ErrNotFound) {
				continue
			}
			loadErrs = append(loadErrs, fmt.Sprintf("%s: %v", id, err))
			continue
		}
		i.Placement = &api.Placement{
			Region: s.config.Region,
			Zone:   s.config.Zone,
		}
		instances = append(instances, i)
	}

	if len(loadErrs) > 0 {
		return instances, fmt.Errorf("failed to load %d instance config(s): %s", len(loadErrs), loadErrs)
	}
	return instances, nil
}

// listInstanceConfigs returns instances loaded from on-disk JSON only — no
// VMM state probe, no storage (zvol volsize) lookup. This is dramatically
// cheaper than listInstances: it avoids two `zfs` subprocess execs and one
// VMM socket dial per instance. The State and VMConfig.Disk fields reflect
// whatever was last persisted, which lags reality during transient
// operations (start/stop/grow); callers that need live values must use
// listInstances.
//
// Same partial-list contract as listInstances: a non-nil error alongside a
// non-empty slice means "some configs were unreadable"; a non-nil error
// with a nil slice means "enumeration itself failed" (e.g. Glob error).
func (s *Service) listInstanceConfigs(ctx context.Context) ([]*api.Instance, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	configs, err := filepath.Glob(filepath.Join(s.getInstanceDir("*"), "config.json"))
	if err != nil {
		return nil, err
	}
	instances := make([]*api.Instance, 0, len(configs))
	var loadErrs []string
	for _, config := range configs {
		id := filepath.Base(filepath.Dir(config))
		i, err := s.loadInstanceConfig(id)
		if err != nil {
			if errors.Is(err, api.ErrNotFound) {
				// Race: Glob saw the dir, DeleteInstance removed it.
				continue
			}
			loadErrs = append(loadErrs, fmt.Sprintf("%s: %v", id, err))
			continue
		}
		instances = append(instances, i)
	}
	if len(loadErrs) > 0 {
		return instances, fmt.Errorf("failed to load %d instance config(s): %s", len(loadErrs), loadErrs)
	}
	return instances, nil
}
