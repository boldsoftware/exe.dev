package storage

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"google.golang.org/grpc"
	"tailscale.com/util/singleflight"

	"exe.dev/exelet/config"
	"exe.dev/exelet/services"
	"exe.dev/exelet/utils"
	api "exe.dev/pkg/api/exe/storage/v1"
)

type Service struct {
	api.UnimplementedStorageServiceServer
	config         *config.ExeletConfig
	context        *services.ServiceContext
	mu             *sync.Mutex
	log            *slog.Logger
	imageLoadGroup singleflight.Group[string, string]
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

// LoadImage implements services.ImageLoader with singleflight coordination.
// All image loads go through this method to ensure only one load per digest.
func (s *Service) LoadImage(ctx context.Context, imageRef, platform string) (string, error) {
	// Fetch manifest to get digest for singleflight key
	imageMetadata, err := s.context.ImageManager.FetchManifestForPlatform(ctx, imageRef, platform)
	if err != nil {
		return "", fmt.Errorf("failed to fetch manifest: %w", err)
	}
	imageFSID := imageMetadata.Digest

	// Singleflight ensures only one load per digest.
	// Pass the metadata to LoadImageWithMetadata so it uses the same digest
	// for both the singleflight key and the actual fetch (avoiding races if tag moves).
	imageID, err, _ := s.imageLoadGroup.Do(imageFSID, func() (string, error) {
		return utils.LoadImageWithMetadata(ctx, imageRef, platform, imageMetadata, s.context.ImageManager, s.context.StorageManager, s.log)
	})
	return imageID, err
}
