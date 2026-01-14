package compute

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"exe.dev/exelet/vmm"
	api "exe.dev/pkg/api/exe/compute/v1"
)

func (s *Service) StartInstance(ctx context.Context, req *api.StartInstanceRequest) (*api.StartInstanceResponse, error) {
	// Check if instance is being migrated
	if err := s.checkNotMigrating(req.ID); err != nil {
		return nil, status.Error(codes.FailedPrecondition, err.Error())
	}

	i, err := s.getInstance(ctx, req.ID)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	switch i.State {
	case api.VMState_RUNNING:
		// nothing; already running
	case api.VMState_STOPPED:
		// start
		if err := s.startInstance(ctx, req.ID); err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}
	default:
		return nil, status.Error(codes.FailedPrecondition, "instance in an invalid state to start")
	}
	return &api.StartInstanceResponse{}, nil
}

func (s *Service) startInstance(ctx context.Context, id string) error {
	// get instance config to update state and config
	iCfg, err := s.loadInstanceConfig(id)
	if err != nil {
		return err
	}
	// ensure disk is loaded
	instanceFS, err := s.context.StorageManager.Load(ctx, id)
	if err != nil {
		return err
	}

	if iCfg.VMConfig == nil {
		return fmt.Errorf("unexpected nil VMConfig for %s", id)
	}

	// update disk path
	iCfg.VMConfig.RootDiskPath = instanceFS.Path
	// update state
	iCfg.State = api.VMState_STARTING
	if err := s.saveInstanceConfig(iCfg); err != nil {
		return err
	}

	vmmgr, err := vmm.NewVMM(s.config.RuntimeAddress, s.context.NetworkManager, s.config.EnableHugepages, s.log)
	if err != nil {
		return err
	}

	// Get or create VMM config (migrated VMs won't have a VMM config yet)
	vmCfg, err := vmmgr.Get(ctx, id)
	isMigratedVM := false
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("failed to get VMM config: %w", err)
		}
		// VMM config doesn't exist - this is a migrated VM being started for the first time
		s.log.DebugContext(ctx, "creating VMM config for migrated instance", "id", id)
		vmCfg = iCfg.VMConfig
		isMigratedVM = true
	}

	// Create network interface
	networkInterface, err := s.context.NetworkManager.CreateInterface(ctx, vmCfg.ID)
	if err != nil {
		return err
	}
	// update network interface (ip= boot arg is derived from this at runtime)
	vmCfg.NetworkInterface = networkInterface

	if isMigratedVM {
		// For migrated VMs, create the VMM config with network interface already set
		if err := vmmgr.Create(ctx, vmCfg); err != nil {
			return fmt.Errorf("failed to create VMM config: %w", err)
		}
	} else {
		// For existing VMs, update the config
		if err := vmmgr.Update(ctx, vmCfg); err != nil {
			return err
		}
	}

	// start
	if err := vmmgr.Start(ctx, id); err != nil {
		return err
	}

	// get SSH port from instance config (persisted from creation)
	sshPort := int(iCfg.SSHPort)
	if sshPort == 0 {
		// shouldn't happen for instances created with new code, but allocate if needed
		var err error
		sshPort, err = s.portAllocator.Allocate()
		if err != nil {
			return fmt.Errorf("failed to allocate SSH port: %w", err)
		}
		// update config with newly allocated port
		iCfg.SSHPort = int32(sshPort)
	}

	// parse VM IP from network interface
	vmIP := ""
	if networkInterface.IP != nil && networkInterface.IP.IPV4 != "" {
		ipAddr, _, err := net.ParseCIDR(networkInterface.IP.IPV4)
		if err != nil {
			return fmt.Errorf("failed to parse VM IP: %w", err)
		}
		vmIP = ipAddr.String()
	} else {
		return fmt.Errorf("no IP address assigned to VM")
	}

	// create and start SSH proxy using socat
	instanceDir := s.getInstanceDir(id)
	s.log.DebugContext(ctx, "starting SSH proxy", "instance", id, "port", sshPort, "target", fmt.Sprintf("%s:22", vmIP))
	if err := s.proxyManager.CreateProxy(id, vmIP, sshPort, instanceDir); err != nil {
		return fmt.Errorf("failed to start SSH proxy: %w", err)
	}

	// update network config
	iCfg.VMConfig.NetworkInterface = networkInterface
	iCfg.State = api.VMState_RUNNING
	if err := s.saveInstanceConfig(iCfg); err != nil {
		return err
	}

	return nil
}
