package compute

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"google.golang.org/grpc"

	"exe.dev/exelet/config"
	"exe.dev/exelet/services"
	api "exe.dev/pkg/api/exe/compute/v1"
)

const (
	instanceDataDir = "instances"
)

var (
	updateInterval = time.Second * 10
)

type Service struct {
	api.UnimplementedComputeServiceServer
	config       *config.ExeletConfig
	context      *services.ServiceContext
	mu           *sync.Mutex
	updateTicker *time.Ticker
	log          *slog.Logger
}

// New returns a new service.
func New(cfg *config.ExeletConfig, log *slog.Logger) (services.Service, error) {
	return &Service{
		config:       cfg,
		mu:           &sync.Mutex{},
		updateTicker: time.NewTicker(updateInterval),
		log:          log,
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

	// start instances
	if s.config.EnableInstanceBootOnStartup {
		s.log.Info("booting local instances")
		instances, err := s.listInstances(ctx)
		if err != nil {
			return err
		}

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
	s.updateTicker.Stop()

	return nil
}
