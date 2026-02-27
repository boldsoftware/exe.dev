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
	stopLogRotation     func()
	migratingInstances  sync.Map // map[instanceID]struct{} - instances currently being migrated
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

	// Apply connection limits and bandwidth limits in the background.
	// This allows the gRPC server to start faster.
	go func() {
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
		s.log.InfoContext(ctx, "background network limits applied", "count", len(instances))
	}()

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

	// Start boot log rotation
	interval := s.config.BootLogRotationInterval
	if interval == 0 {
		interval = config.DefaultBootLogRotationInterval
	}
	maxBytes := s.config.BootLogMaxBytes
	if maxBytes == 0 {
		maxBytes = config.DefaultBootLogMaxBytes
	}
	keepBytes := s.config.BootLogKeepBytes
	if keepBytes == 0 {
		keepBytes = config.DefaultBootLogKeepBytes
	}
	s.stopLogRotation = vmm.StartLogRotation(ctx, interval, maxBytes, keepBytes)

	// Ensure rotation is stopped if Start fails after this point
	startSucceeded := false
	defer func() {
		if !startSucceeded && s.stopLogRotation != nil {
			s.stopLogRotation()
			s.stopLogRotation = nil
		}
	}()

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

	startSucceeded = true
	return nil
}

// Stop stops the service.
func (s *Service) Stop(ctx context.Context) error {
	// NOTE: We intentionally do NOT stop SSH proxies here.
	// Socat processes run in their own process group and survive exelet restarts.
	// On next startup, RecoverProxies will adopt still-running proxies seamlessly,
	// avoiding any SSH connectivity gap during restarts.

	if s.stopLogRotation != nil {
		s.stopLogRotation()
	}

	return nil
}

// lockForMigration locks an instance for migration, preventing other operations.
// Returns an error if the instance is already being migrated.
func (s *Service) lockForMigration(id string) error {
	if _, loaded := s.migratingInstances.LoadOrStore(id, struct{}{}); loaded {
		return api.ErrMigrating
	}
	return nil
}

// unlockMigration unlocks an instance after migration completes or fails.
func (s *Service) unlockMigration(id string) {
	s.migratingInstances.Delete(id)
}

// checkNotMigrating returns an error if the instance is currently being migrated.
func (s *Service) checkNotMigrating(id string) error {
	if _, ok := s.migratingInstances.Load(id); ok {
		return api.ErrMigrating
	}
	return nil
}
