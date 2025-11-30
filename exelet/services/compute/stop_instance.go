package compute

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"exe.dev/exelet/vmm"
	api "exe.dev/pkg/api/exe/compute/v1"
)

func (s *Service) StopInstance(ctx context.Context, req *api.StopInstanceRequest) (*api.StopInstanceResponse, error) {
	i, err := s.getInstance(ctx, req.ID)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	switch i.State {
	case api.VMState_STOPPED:
		// nothing; already running
	case api.VMState_RUNNING:
		// stop
		if err := s.stopInstance(ctx, req.ID); err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}
	default:
		return nil, status.Error(codes.FailedPrecondition, "instance in an invalid state to stop")
	}
	return &api.StopInstanceResponse{}, nil
}

func (s *Service) stopInstance(ctx context.Context, id string) error {
	vmm, err := vmm.NewVMM(s.config.RuntimeAddress, s.context.NetworkManager, s.log)
	if err != nil {
		return err
	}

	vmCfg, err := vmm.Get(ctx, id)
	if err != nil {
		return err
	}

	if err := vmm.Stop(ctx, id); err != nil {
		return err
	}

	// extract IP before clearing network interface
	ip := ""
	if vmCfg.NetworkInterface != nil && vmCfg.NetworkInterface.IP != nil {
		ip = vmCfg.NetworkInterface.IP.IPV4
	}

	// update vm config
	vmCfg.NetworkInterface = nil
	if err := vmm.Update(ctx, vmCfg); err != nil {
		return err
	}

	// delete network interface and release DHCP lease
	if err := s.context.NetworkManager.DeleteInterface(ctx, id, ip); err != nil {
		return err
	}

	// stop SSH proxy (but keep port allocated for next start)
	if _, err := s.proxyManager.StopProxy(id); err != nil {
		s.log.WarnContext(ctx, "failed to remove SSH proxy", "instance", id, "error", err)
	} else {
		s.log.DebugContext(ctx, "stopped SSH proxy", "instance", id)
	}

	// update instance config
	iCfg, err := s.loadInstanceConfig(id)
	if err != nil {
		return err
	}
	// update state
	iCfg.State = api.VMState_STOPPED
	// remove network config
	iCfg.VMConfig.NetworkInterface = nil
	s.log.DebugContext(ctx, "updating instance config", "config", iCfg.VMConfig)
	if err := s.saveInstanceConfig(iCfg); err != nil {
		return err
	}

	return nil
}
