package compute

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"sync"

	"google.golang.org/grpc"
	"tailscale.com/util/singleflight"

	"exe.dev/exelet/config"
	"exe.dev/exelet/services"
	"exe.dev/exelet/sshproxy"
	"exe.dev/exelet/vmm"
	api "exe.dev/pkg/api/exe/compute/v1"
)

const (
	instanceDataDir = "instances"
)

type Service struct {
	api.UnimplementedComputeServiceServer
	config              *config.ExeletConfig
	context             *services.ServiceContext
	mu                  *sync.Mutex
	log                 *slog.Logger
	portAllocator       *PortAllocator
	proxyManager        *sshproxy.Manager
	instanceCreateGroup singleflight.Group[string, *api.Instance]
	instanceDeleteGroup singleflight.Group[string, *api.DeleteInstanceResponse]
}

// New returns a new service.
func New(cfg *config.ExeletConfig, log *slog.Logger) (services.Service, error) {
	// Use configured port range, or defaults if not set
	minPort := cfg.ProxyPortMin
	maxPort := cfg.ProxyPortMax
	var portAllocator *PortAllocator
	if minPort > 0 && maxPort > 0 {
		portAllocator = NewPortAllocatorWithRange(minPort, maxPort)
	} else {
		portAllocator = NewPortAllocator()
	}

	return &Service{
		config:        cfg,
		mu:            &sync.Mutex{},
		log:           log,
		portAllocator: portAllocator,
		proxyManager:  sshproxy.NewManager(cfg.DataDir, cfg.ProxyBindIP, log),
	}, nil
}

// Register is called from the server to register with the GRPC server.
func (s *Service) Register(ctx *services.ServiceContext, server *grpc.Server) error {
	if ctx.ImageLoader == nil {
		return errors.New("compute service requires ImageLoader to be set in ServiceContext")
	}
	api.RegisterComputeServiceServer(server, s)
	s.context = ctx
	return nil
}

// Type is the type of service.
func (s *Service) Type() services.Type {
	return services.ComputeService
}

// Requires defines what other services on which this service depends.
func (s *Service) Requires() []services.Type {
	return nil
}

// Start runs the service.
func (s *Service) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Load existing instances
	instances, err := s.listInstances(ctx)
	if err != nil {
		return err
	}

	// Mark SSH ports as allocated in the port allocator
	for _, i := range instances {
		if i.SSHPort > 0 {
			s.portAllocator.MarkAllocated(int(i.SSHPort))
			s.log.DebugContext(ctx, "marked port as allocated", "instance", i.ID, "port", i.SSHPort)
		}
	}

	// Apply connection limits and bandwidth limits for existing running instances
	for _, i := range instances {
		// Skip stopped instances - they don't have TAP devices
		if i.State == api.VMState_STOPPED || i.State == api.VMState_CREATING {
			continue
		}

		if i.VMConfig != nil && i.VMConfig.NetworkInterface != nil && i.VMConfig.NetworkInterface.IP != nil {
			ipStr := i.VMConfig.NetworkInterface.IP.IPV4
			ip, _, err := net.ParseCIDR(ipStr)
			if err != nil {
				s.log.WarnContext(ctx, "failed to parse instance IP", "instance", i.ID, "ip", ipStr, "error", err)
				continue
			}
			if err := s.context.NetworkManager.ApplyConnectionLimit(ctx, ip.String()); err != nil {
				s.log.WarnContext(ctx, "failed to apply connection limit", "instance", i.ID, "ip", ip.String(), "error", err)
			}
		}
		// Apply bandwidth limit to existing TAP device
		if err := s.context.NetworkManager.ApplyBandwidthLimit(ctx, i.ID); err != nil {
			s.log.WarnContext(ctx, "failed to apply bandwidth limit", "instance", i.ID, "error", err)
		}
	}

	// Recover existing SSH proxies from disk
	// This will find existing socat processes and adopt them, or restart dead ones
	if err := s.proxyManager.RecoverProxies(instances); err != nil {
		s.log.WarnContext(ctx, "failed to recover SSH proxies", "error", err)
		// Don't fail startup, continue
	}

	// Recover existing VMM processes (cloud-hypervisor and virtiofsd)
	// This will adopt any still-running processes and clean up stale metadata
	vmm, err := vmm.NewVMM(s.config.RuntimeAddress, s.context.NetworkManager, s.config.EnableHugepages, s.log)
	if err != nil {
		return err
	}
	if err := vmm.RecoverProcesses(ctx); err != nil {
		s.log.WarnContext(ctx, "failed to recover VMM processes", "error", err)
		// Don't fail startup, continue
	}

	// start stopped instances if enabled
	if s.config.EnableInstanceBootOnStartup {
		s.log.InfoContext(ctx, "booting stopped instances")
		for _, i := range instances {
			if i.State == api.VMState_STOPPED {
				if err := s.startInstance(ctx, i.ID); err != nil {
					return err
				}
			}
		}
	}

	return nil
}

// Stop stops the service.
func (s *Service) Stop(ctx context.Context) error {
	s.proxyManager.StopAll()

	return nil
}
