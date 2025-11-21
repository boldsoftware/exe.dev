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
	// use singleflight to ensure only one delete per instance
	resp, err, _ := s.instanceDeleteGroup.Do(req.ID, func() (*api.DeleteInstanceResponse, error) {
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

		// stop and remove SSH proxy (needs mutex for service-level resources)
		s.mu.Lock()
		if port, err := s.proxyManager.StopProxy(instance.ID); err != nil {
			s.log.WarnContext(ctx, "failed to remove SSH proxy", "instance", instance.ID, "error", err)
		} else {
			s.portAllocator.Release(port)
			s.log.DebugContext(ctx, "removed SSH proxy", "instance", instance.ID, "port", port)
		}
		s.mu.Unlock()

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
	})
	return resp, err
}
