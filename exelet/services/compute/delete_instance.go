package compute

import (
	"context"
	"os"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"exe.dev/exelet/vmm"
	api "exe.dev/pkg/api/exe/compute/v1"
)

func (s *Service) DeleteInstance(ctx context.Context, req *api.DeleteInstanceRequest) (*api.DeleteInstanceResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	resp, err := s.GetInstance(ctx, &api.GetInstanceRequest{ID: req.ID})
	if err != nil {
		return nil, status.Error(codes.FailedPrecondition, err.Error())
	}

	instance := resp.Instance

	vmm, err := vmm.NewVMM(s.config.RuntimeAddress, s.config.NetworkManagerAddress, s.log)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	// stop vm
	if err := vmm.Stop(ctx, instance.ID); err != nil {
		return nil, status.Errorf(codes.Internal, "error stopping vm: %s", err)
	}

	// delete vm
	if err := vmm.Delete(ctx, instance.ID); err != nil {
		return nil, status.Errorf(codes.Internal, "error deleting vm: %s", err)
	}

	// remove instance filesystem
	if err := s.context.StorageManager.Delete(ctx, instance.ID); err != nil {
		return nil, status.Errorf(codes.Internal, "error removing instance fs: %s", err)
	}

	// remove instance data
	if err := os.RemoveAll(s.getInstanceDir(instance.ID)); err != nil {
		return nil, status.Errorf(codes.Internal, "error removing instance state dir: %s", err)
	}

	// TODO: publish event

	return &api.DeleteInstanceResponse{}, nil
}
