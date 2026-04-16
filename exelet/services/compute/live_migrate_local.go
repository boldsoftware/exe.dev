package compute

import (
	"context"
	"time"

	"github.com/grpc-ecosystem/go-grpc-middleware/v2/interceptors/logging"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	api "exe.dev/pkg/api/exe/compute/v1"
)

func (s *Service) LiveMigrateLocal(ctx context.Context, req *api.LiveMigrateLocalRequest) (*api.LiveMigrateLocalResponse, error) {
	logging.AddFields(ctx, logging.Fields{"container_id", req.InstanceID})

	if req.InstanceID == "" {
		return nil, status.Error(codes.InvalidArgument, "instance_id is required")
	}

	// Use the migration flag (not the per-instance lifecycle lock) so concurrent
	// lifecycle ops fail fast with ErrMigrating rather than blocking for the
	// entire migration. Matches the cross-host migration convention.
	if err := s.lockForMigration(req.InstanceID); err != nil {
		return nil, status.Error(codes.FailedPrecondition, err.Error())
	}
	defer s.unlockMigration(req.InstanceID)

	i, err := s.getInstance(ctx, req.InstanceID)
	if err != nil {
		return nil, status.Error(codes.NotFound, err.Error())
	}

	if i.State != api.VMState_RUNNING {
		return nil, status.Errorf(codes.FailedPrecondition, "VM must be running, current state: %s", i.State)
	}

	s.log.InfoContext(ctx, "live migrate local: starting", "instance", req.InstanceID)

	result, err := s.vmm.LiveMigrateLocal(ctx, req.InstanceID)
	if err != nil {
		// Migration failed — the old CH process may be in a bad state.
		// Cold restart the VM so the user isn't left with a dead instance.
		s.log.ErrorContext(ctx, "live migrate local failed, cold restarting",
			"instance", req.InstanceID, "error", err)

		// Call the internal helpers, not StopInstance/StartInstance — those would
		// see our own migrating flag (set by lockForMigration above) and return
		// ErrMigrating. The helpers do not check the flag and do not take the
		// per-instance lock, so they're safe to call here.
		coldRestartStart := time.Now()
		if stopErr := s.stopInstance(ctx, req.InstanceID); stopErr != nil {
			s.log.ErrorContext(ctx, "cold restart: stop failed",
				"instance", req.InstanceID, "error", stopErr)
		}
		if startErr := s.startInstance(ctx, req.InstanceID); startErr != nil {
			s.log.ErrorContext(ctx, "cold restart: start failed",
				"instance", req.InstanceID, "error", startErr)
			return nil, status.Errorf(codes.Internal, "live migrate failed and cold restart failed: migrate=%v, start=%v", err, startErr)
		}
		coldRestartDowntime := time.Since(coldRestartStart)

		s.log.InfoContext(ctx, "live migrate local: completed via cold restart",
			"instance", req.InstanceID, "migrate_error", err, "downtime_ms", coldRestartDowntime.Milliseconds())
		return &api.LiveMigrateLocalResponse{
			Outcome:        api.LiveMigrateLocalResponse_COLD_RESTARTED,
			DowntimeMs:     coldRestartDowntime.Milliseconds(),
			MigrationError: err.Error(),
		}, nil
	}

	s.log.InfoContext(ctx, "live migrate local: completed",
		"instance", req.InstanceID, "downtime_ms", result.Downtime.Milliseconds())
	return &api.LiveMigrateLocalResponse{
		Outcome:    api.LiveMigrateLocalResponse_LIVE_MIGRATED,
		DowntimeMs: result.Downtime.Milliseconds(),
	}, nil
}
