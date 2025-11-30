package compute

import (
	"context"
	"fmt"
	"os"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"exe.dev/exelet/vmm"
	api "exe.dev/pkg/api/exe/compute/v1"
)

func (s *Service) GetInstance(ctx context.Context, req *api.GetInstanceRequest) (*api.GetInstanceResponse, error) {
	i, err := s.getInstance(ctx, req.ID)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &api.GetInstanceResponse{
		Instance: i,
	}, nil
}

func (s *Service) getInstance(ctx context.Context, id string) (*api.Instance, error) {
	configPath := s.getInstanceConfigPath(id)
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: instance %s", api.ErrNotFound, id)
		}
		return nil, nil
	}
	i := &api.Instance{}
	if err := i.Unmarshal(data); err != nil {
		return nil, err
	}

	// check state
	vmm, err := vmm.NewVMM(s.config.RuntimeAddress, s.context.NetworkManager, s.log)
	if err != nil {
		return nil, err
	}

	state, err := vmm.State(ctx, id)
	if err != nil {
		return nil, err
	}

	i.State = state

	return i, nil
}
