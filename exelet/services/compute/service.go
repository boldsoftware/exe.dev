package compute

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"sync"

	"google.golang.org/grpc"
	"tailscale.com/util/singleflight"

	"exe.dev/exelet/config"
	"exe.dev/exelet/services"
	api "exe.dev/pkg/api/exe/compute/v1"
	"exe.dev/pkg/tcpproxy"
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
	proxyManager        *tcpproxy.ProxyManager
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
		proxyManager:  tcpproxy.NewProxyManager(log),
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

	// Load existing instances and mark their ports as allocated
	instances, err := s.listInstances(ctx)
	if err != nil {
		return err
	}

	for _, i := range instances {
		// Mark the SSH port as allocated in the port allocator
		if i.SSHPort > 0 {
			s.portAllocator.MarkAllocated(int(i.SSHPort))
			s.log.DebugContext(ctx, "marked port as allocated", "instance", i.ID, "port", i.SSHPort)
		}

		// Recreate TCP proxies for instances that are already RUNNING
		// (e.g., if the VM didn't stop when exelet restarted)
		if i.State == api.VMState_RUNNING && i.SSHPort > 0 {
			if err := s.recreateProxyForInstance(ctx, i); err != nil {
				s.log.WarnContext(ctx, "failed to recreate proxy for running instance", "instance", i.ID, "error", err)
				// Continue with other instances rather than failing startup
				continue
			}
		}
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

// recreateProxyForInstance recreates the TCP proxy for a running instance
// This is called during service startup to restore proxies after an exelet restart
func (s *Service) recreateProxyForInstance(ctx context.Context, i *api.Instance) error {
	// Check if proxy already exists
	if _, exists := s.proxyManager.GetPort(i.ID); exists {
		s.log.DebugContext(ctx, "proxy already exists for instance", "instance", i.ID)
		return nil
	}

	// Parse VM IP from network interface
	if i.VMConfig == nil || i.VMConfig.NetworkInterface == nil || i.VMConfig.NetworkInterface.IP == nil {
		return fmt.Errorf("instance %s has no network interface configured", i.ID)
	}

	vmIP := ""
	if i.VMConfig.NetworkInterface.IP.IPV4 != "" {
		ipAddr, _, err := net.ParseCIDR(i.VMConfig.NetworkInterface.IP.IPV4)
		if err != nil {
			return fmt.Errorf("failed to parse VM IP: %w", err)
		}
		vmIP = ipAddr.String()
	} else {
		return fmt.Errorf("no IP address assigned to VM %s", i.ID)
	}

	sshPort := int(i.SSHPort)
	s.log.InfoContext(ctx, "recreating SSH proxy for running instance", "instance", i.ID, "port", sshPort, "target", fmt.Sprintf("%s:22", vmIP))

	// Create and start TCP proxy
	proxy := tcpproxy.NewTCPProxy(sshPort, vmIP, 22, s.log)
	if err := proxy.Start(); err != nil {
		return fmt.Errorf("failed to start SSH proxy: %w", err)
	}
	s.proxyManager.AddProxy(i.ID, proxy, sshPort)

	return nil
}

// Stop stops the service.
func (s *Service) Stop(ctx context.Context) error {
	s.proxyManager.StopAll()

	return nil
}
