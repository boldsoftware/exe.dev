package compute

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"strings"

	"github.com/grpc-ecosystem/go-grpc-middleware/v2/interceptors/logging"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	api "exe.dev/pkg/api/exe/compute/v1"
)

func (s *Service) StartInstance(ctx context.Context, req *api.StartInstanceRequest) (*api.StartInstanceResponse, error) {
	logging.AddFields(ctx, logging.Fields{"container_id", req.ID})

	// Serialize per-instance operations to prevent concurrent Start+Stop/Delete races.
	// Migration check must be under this lock to prevent TOCTOU with lockForMigration.
	unlock := s.lockInstance(req.ID)
	defer unlock()

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

func (s *Service) startInstance(ctx context.Context, id string) (retErr error) {
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

	// Get or create VMM config (migrated VMs won't have a VMM config yet)
	vmCfg, err := s.vmm.Get(ctx, id)
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
	// Rollback on failure to prevent IP leaks and live-but-disconnected VMs.
	// Before s.vmm.Start, only the network interface needs cleanup.
	// After s.vmm.Start, we must also stop the VM before releasing networking.
	vmStarted := false
	defer func() {
		if retErr == nil {
			return
		}
		if vmStarted {
			if stopErr := s.vmm.Stop(ctx, id); stopErr != nil {
				// VM may still be running — do NOT tear down networking or
				// we create a live-but-disconnected guest. Leave the IP
				// allocated; the reconciler will clean up once the VM is
				// eventually stopped or the host restarts.
				s.log.WarnContext(ctx, "failed to stop VM after start failure, leaving network intact for reconciliation",
					"id", id, "error", stopErr)
				return
			}
		}
		ip := ""
		if networkInterface.IP != nil {
			ip = networkInterface.IP.IPV4
			if idx := strings.Index(ip, "/"); idx > 0 {
				ip = ip[:idx]
			}
		}
		if delErr := s.context.NetworkManager.DeleteInterface(ctx, vmCfg.ID, ip); delErr != nil {
			s.log.WarnContext(ctx, "failed to clean up network interface after start failure", "id", id, "error", delErr)
		}
	}()
	// update network interface (ip= boot arg is derived from this at runtime)
	vmCfg.NetworkInterface = networkInterface

	if isMigratedVM {
		// For migrated VMs, create the VMM config with network interface already set
		if err := s.vmm.Create(ctx, vmCfg); err != nil {
			return fmt.Errorf("failed to create VMM config: %w", err)
		}
	} else {
		// For existing VMs, update the config
		if err := s.vmm.Update(ctx, vmCfg); err != nil {
			return err
		}
	}

	// start
	if err := s.vmm.Start(ctx, id); err != nil {
		return err
	}
	vmStarted = true

	// get SSH port from instance config (persisted from creation)
	sshPort := int(iCfg.SSHPort)
	if sshPort == 0 {
		// shouldn't happen for instances created with new code, but allocate if needed
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
	if err := s.proxyManager.CreateProxy(ctx, id, vmIP, sshPort, instanceDir); err != nil {
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
