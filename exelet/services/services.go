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
	// ReplicationService is the service that manages storage replication
	ReplicationService Type = "exe.services.replication.v1"
	// PktFlowService collects per-tap network stats for abuse detection
	PktFlowService Type = "exe.services.pktflow.v1"
)

// InstanceLookup provides a method to look up instances by IP address
type InstanceLookup interface {
	GetInstanceByIP(ctx context.Context, ip string) (id, name string, err error)
	Instances(ctx context.Context) ([]*api.Instance, error)
	// GetInstanceByID returns detailed information about a specific instance
	GetInstanceByID(ctx context.Context, id string) (*api.Instance, error)
	// StopInstanceByID stops a running instance
	StopInstanceByID(ctx context.Context, id string) error
	// StartInstanceByID starts a stopped instance
	StartInstanceByID(ctx context.Context, id string) error
}

// ImageLoader provides image loading with singleflight coordination
type ImageLoader interface {
	LoadImage(ctx context.Context, imageRef, platform string) (string, error)
}

// ReplicationSuspender allows services to temporarily exclude volumes from replication.
// This is used during migration to prevent the replication worker from snapshotting
// a dataset that is being received via zfs recv.
type ReplicationSuspender interface {
	SuspendVolume(volumeID string)
	ResumeVolume(volumeID string)
	IsVolumeActive(volumeID string) bool
	WaitVolumeIdle(ctx context.Context, volumeID string)
}

// MemoryReclaimer allows services to request proactive memory reclamation.
// This is used during live migration to free physical memory on the target host
// by pushing idle VM pages to swap before restoring the migrating VM.
type MemoryReclaimer interface {
	ReclaimMemory(ctx context.Context, bytes uint64) error
}

type ServiceContext struct {
	StorageManager       storage.StorageManager
	NetworkManager       network.NetworkManager
	ImageManager         *image.ImageManager
	ComputeService       InstanceLookup
	ImageLoader          ImageLoader
	MetricsRegistry      *prometheus.Registry
	ReplicationSuspender ReplicationSuspender
	MemoryReclaimer      MemoryReclaimer
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
