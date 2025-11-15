package compute

import (
	"context"
	"log/slog"
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
	return &Service{
		config:        cfg,
		mu:            &sync.Mutex{},
		log:           log,
		portAllocator: NewPortAllocator(),
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
