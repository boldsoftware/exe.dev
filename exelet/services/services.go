package services

import (
	"context"

	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc"

	"exe.dev/deps/image"
	"exe.dev/exelet/network"
	"exe.dev/exelet/storage"
	api "exe.dev/pkg/api/exe/compute/v1"
)

type Type string

const (
	// ComputeService is the service that implements compute
	ComputeService Type = "exe.services.compute.v1"
	// StorageService is the service that implements storage
	StorageService Type = "exe.services.storage.v1"
	// ResourceManagerService is the service that manages resource quotas and priorities
	ResourceManagerService Type = "exe.services.resource_manager.v1"
)

// InstanceLookup provides a method to look up instances by IP address
type InstanceLookup interface {
	GetInstanceByIP(ctx context.Context, ip string) (id, name string, err error)
	Instances(ctx context.Context) ([]*api.Instance, error)
}

// ImageLoader provides image loading with singleflight coordination
type ImageLoader interface {
	LoadImage(ctx context.Context, imageRef, platform string) (string, error)
}

type ServiceContext struct {
	StorageManager  storage.StorageManager
	NetworkManager  network.NetworkManager
	ImageManager    *image.ImageManager
	ComputeService  InstanceLookup
	ImageLoader     ImageLoader
	MetricsRegistry *prometheus.Registry
}

// Service is the interface that all services must implement
type Service interface {
	// Type returns the type that the service provides
	Type() Type
	// Register registers the service with the GRPC server
	Register(*ServiceContext, *grpc.Server) error
	// Start provides a mechanism to start service specific actions
	Start(context.Context) error
	// Stop provides a mechanism to stop the service
	Stop(context.Context) error
}
