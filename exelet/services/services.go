package services

import (
	"context"

	"google.golang.org/grpc"

	"exe.dev/exelet/storage"
)

type Type string

const (
	// ComputeService is the service that implements compute
	ComputeService Type = "exe.services.compute.v1"
)

type ServiceContext struct {
	StorageManager storage.StorageManager
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
