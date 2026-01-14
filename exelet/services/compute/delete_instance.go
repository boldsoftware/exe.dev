package compute

import (
	"context"
	"os"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"exe.dev/exelet/vmm"
	api "exe.dev/pkg/api/exe/compute/v1"
)

func (s *Service) DeleteInstance(ctx context.Context, req *api.DeleteInstanceRequest) (*api.DeleteInstanceResponse, error) {
	// use singleflight to ensure only one delete per instance
	resp, err, _ := s.instanceDeleteGroup.Do(req.ID, func() (*api.DeleteInstanceResponse, error) {
		// Check if instance is being migrated
		if err := s.checkNotMigrating(req.ID); err != nil {
			return nil, status.Error(codes.FailedPrecondition, err.Error())
		}

		// Use WithoutCancel so the delete completes even if the gRPC request times out or client disconnects
		ctx := context.WithoutCancel(ctx)

		resp, err := s.GetInstance(ctx, &api.GetInstanceRequest{ID: req.ID})
		if err != nil {
			// Pass through NotFound from GetInstance so callers can handle gracefully
			if s, ok := status.FromError(err); ok && s.Code() == codes.NotFound {
				return nil, err
			}
			return nil, status.Error(codes.FailedPrecondition, err.Error())
		}

		instance := resp.Instance

		vmm, err := vmm.NewVMM(s.config.RuntimeAddress, s.context.NetworkManager, s.config.EnableHugepages, s.log)
		if err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}

		// stop vm (continue even if stop fails - instance might already be stopped)
		if err := vmm.Stop(ctx, instance.ID); err != nil {
			s.log.WarnContext(ctx, "error stopping vm during delete, continuing with cleanup", "instance", instance.ID, "error", err)
		}

		// extract IP from instance for DHCP release (strip CIDR suffix)
		ip := ""
		if instance.VMConfig != nil && instance.VMConfig.NetworkInterface != nil && instance.VMConfig.NetworkInterface.IP != nil {
			ip = instance.VMConfig.NetworkInterface.IP.IPV4
			if idx := strings.Index(ip, "/"); idx > 0 {
				ip = ip[:idx]
			}
		}

		// delete vm
		if err := vmm.Delete(ctx, instance.ID, ip); err != nil {
			return nil, status.Errorf(codes.Internal, "error deleting vm: %s", err)
		}

		// stop and remove SSH proxy (needs mutex for service-level resources)
		s.mu.Lock()
		if _, err := s.proxyManager.StopProxy(instance.ID); err != nil {
			s.log.WarnContext(ctx, "failed to remove SSH proxy", "instance", instance.ID, "error", err)
		}
		// Always release the port from allocator, even if proxy stop failed
		// The port was allocated during creation and must be freed
		if instance.SSHPort > 0 {
			s.portAllocator.Release(int(instance.SSHPort))
			s.log.DebugContext(ctx, "released SSH port", "instance", instance.ID, "port", instance.SSHPort)
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
