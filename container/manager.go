package container

import (
	"context"
	"fmt"
	"io"
)

// Manager provides container lifecycle management operations
type Manager interface {
	// Container lifecycle
	CreateContainer(ctx context.Context, req *CreateContainerRequest) (*Container, error)
	GetContainer(ctx context.Context, userID, containerID string) (*Container, error)
	ListContainers(ctx context.Context, userID string) ([]*Container, error)
	StartContainer(ctx context.Context, userID, containerID string) error
	StopContainer(ctx context.Context, userID, containerID string) error
	DeleteContainer(ctx context.Context, userID, containerID string) error

	// Image building
	BuildImage(ctx context.Context, req *BuildRequest) (*BuildResult, error)
	GetBuildStatus(ctx context.Context, buildID string) (*BuildResult, error)

	// Container access
	GetContainerLogs(ctx context.Context, userID, containerID string, lines int) ([]string, error)
	ConnectToContainer(ctx context.Context, userID, containerID string) (*ContainerConnection, error)
	ExecuteInContainer(ctx context.Context, userID, containerID string, cmd []string, stdin io.Reader, stdout, stderr io.Writer) error

	// Cleanup and maintenance
	Close() error

	// Diagnostics
	GetContainerDiagnostics(ctx context.Context, userID, containerName string) (string, error)
}

// Config holds the configuration for the container manager
type Config struct {
	// Docker hosts - list of DOCKER_HOST values for remote Docker daemons
	// Empty string means local Docker daemon
	DockerHosts []string `json:"docker_hosts"`

	// Default resource limits
	DefaultCPURequest    string `json:"default_cpu_request"`
	DefaultMemoryRequest string `json:"default_memory_request"`
	DefaultStorageSize   string `json:"default_storage_size"`
}

// DefaultConfig returns a sensible default configuration
func DefaultConfig() *Config {
	return &Config{
		DockerHosts:          []string{""}, // Default to local Docker
		DefaultCPURequest:    "100m",
		DefaultMemoryRequest: "256Mi",
		DefaultStorageSize:   "1Gi",
	}
}

// validateConfig ensures all required fields are present
func validateConfig(cfg *Config) error {
	if cfg.DockerHosts == nil || len(cfg.DockerHosts) == 0 {
		return fmt.Errorf("at least one Docker host is required")
	}
	return nil
}
