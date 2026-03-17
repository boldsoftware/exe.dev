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
	configPath := s.getInstanceConfigPath(id)
	data, err := os.ReadFile(configPath)
	if os.IsNotExist(err) {
		return nil, fmt.Errorf("%w: instance %s", api.ErrNotFound, id)
	}
	if err != nil {
		// Return nil, nil on read errors. This handles a tight race where
		// GetInstance is called while the instance is still booting and the
		// config file exists but isn't fully written yet.
		return nil, nil
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

	return i, nil
}
