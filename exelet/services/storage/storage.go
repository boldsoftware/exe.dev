package storage

import (
	"context"
	"log/slog"
	"sync"

	"google.golang.org/grpc"

	"exe.dev/exelet/config"
	"exe.dev/exelet/services"
	api "exe.dev/pkg/api/exe/storage/v1"
)

type Service struct {
	api.UnimplementedStorageServiceServer
	config  *config.ExeletConfig
	context *services.ServiceContext
	mu      *sync.Mutex
	log     *slog.Logger
}

// New returns a new service.
func New(cfg *config.ExeletConfig, log *slog.Logger) (services.Service, error) {
	return &Service{
		config: cfg,
		mu:     &sync.Mutex{},
		log:    log,
	}, nil
}

// Register is called from the server to register with the GRPC server.
func (s *Service) Register(ctx *services.ServiceContext, server *grpc.Server) error {
	api.RegisterStorageServiceServer(server, s)
	s.context = ctx
	return nil
}

// Type is the type of service.
func (s *Service) Type() services.Type {
	return services.StorageService
}

// Requires defines what other services on which this service depends.
func (s *Service) Requires() []services.Type {
	return nil
}

// Start runs the service.
func (s *Service) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return nil
}

// Stop stops the service.
func (s *Service) Stop(ctx context.Context) error {
	return nil
}
