package compute

import (
	"context"
	"errors"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	api "exe.dev/pkg/api/exe/compute/v1"
)

// SetInstanceGroup updates the group ID for an instance.
func (s *Service) SetInstanceGroup(ctx context.Context, req *api.SetInstanceGroupRequest) (*api.SetInstanceGroupResponse, error) {
	if req.ID == "" {
		return nil, status.Error(codes.InvalidArgument, "instance id is required")
	}

	// Load instance config
	inst, err := s.loadInstanceConfig(req.ID)
	if err != nil {
		if errors.Is(err, api.ErrNotFound) {
			return nil, status.Errorf(codes.NotFound, "instance not found: %s", req.ID)
		}
		return nil, status.Errorf(codes.Internal, "failed to load instance config: %v", err)
	}

	// Update group ID
	inst.GroupID = req.GroupID
	inst.UpdatedAt = time.Now().UnixNano()

	// Save config
	if err := s.saveInstanceConfig(inst); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to save config: %v", err)
	}

	s.log.InfoContext(ctx, "instance group updated",
		"id", req.ID,
		"group_id", req.GroupID)

	return &api.SetInstanceGroupResponse{}, nil
}
