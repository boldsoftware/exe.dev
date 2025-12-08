package compute

import (
	"context"
	"log/slog"
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
	imageLoadGroup      singleflight.Group[string, string]
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
		proxyManager:  sshproxy.NewManager(cfg.DataDir, log),
	}, nil
}

// Register is called from the server to register with the GRPC server.
func (s *Service) Register(ctx *services.ServiceContext, server *grpc.Server) error {
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

	// Recover existing SSH proxies from disk
	// This will find existing socat processes and adopt them, or restart dead ones
	// Only recover if we're NOT going to restart instances - when EnableInstanceBootOnStartup
	// is true, startInstance() will create fresh proxies with correct IPs after allocating
	// new network interfaces
	if !s.config.EnableInstanceBootOnStartup {
		if err := s.proxyManager.RecoverProxies(instances); err != nil {
			s.log.WarnContext(ctx, "failed to recover SSH proxies", "error", err)
			// Don't fail startup, continue
		}
	}

	// Recover existing VMM processes (cloud-hypervisor and virtiofsd)
	// This will adopt any still-running processes and clean up stale metadata
	vmm, err := vmm.NewVMM(s.config.RuntimeAddress, s.context.NetworkManager, s.log)
	if err != nil {
		return err
	}
	if err := vmm.RecoverProcesses(ctx); err != nil {
		s.log.WarnContext(ctx, "failed to recover VMM processes", "error", err)
		// Don't fail startup, continue
	}

	// start instances if enabled
	if s.config.EnableInstanceBootOnStartup {
		s.log.InfoContext(ctx, "booting local instances")
		for _, i := range instances {
			if err := s.startInstance(ctx, i.ID); err != nil {
				return err
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
