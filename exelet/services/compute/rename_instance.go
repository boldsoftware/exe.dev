package compute

import (
	"context"
	"errors"
	"time"

	"github.com/grpc-ecosystem/go-grpc-middleware/v2/interceptors/logging"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	api "exe.dev/pkg/api/exe/compute/v1"
)

// RenameInstance updates the name of an instance.
func (s *Service) RenameInstance(ctx context.Context, req *api.RenameInstanceRequest) (*api.RenameInstanceResponse, error) {
	logging.AddFields(ctx, logging.Fields{"container_id", req.ID, "vm_name", req.Name})

	if req.ID == "" {
		return nil, status.Error(codes.InvalidArgument, "instance id is required")
	}
	if req.Name == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}

	// Load instance config
	inst, err := s.loadInstanceConfig(req.ID)
	if errors.Is(err, api.ErrNotFound) {
		return nil, status.Errorf(codes.NotFound, "instance not found: %s", req.ID)
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to load instance config: %v", err)
	}

	// Update name
	inst.Name = req.Name
	inst.UpdatedAt = time.Now().UnixNano()

	// Save config
	if err := s.saveInstanceConfig(inst); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to save config: %v", err)
	}

	s.log.InfoContext(ctx, "instance renamed",
		"id", req.ID,
		"name", req.Name)

	return &api.RenameInstanceResponse{}, nil
}
