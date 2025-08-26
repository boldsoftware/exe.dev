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
	GetContainer(ctx context.Context, allocID, containerID string) (*Container, error)
	ListContainers(ctx context.Context, allocID string) ([]*Container, error)
	StartContainer(ctx context.Context, allocID, containerID string) error
	StopContainer(ctx context.Context, allocID, containerID string) error
	DeleteContainer(ctx context.Context, allocID, containerID string) error

	// Image building
	BuildImage(ctx context.Context, req *BuildRequest) (*BuildResult, error)
	GetBuildStatus(ctx context.Context, buildID string) (*BuildResult, error)

	// Container access
	GetContainerLogs(ctx context.Context, allocID, containerID string, lines int) ([]string, error)
	ConnectToContainer(ctx context.Context, allocID, containerID string) (*ContainerConnection, error)
	ExecuteInContainer(ctx context.Context, allocID, containerID string, cmd []string, stdin io.Reader, stdout, stderr io.Writer) error

	// Cleanup and maintenance
	Close() error

	// Diagnostics
	GetContainerDiagnostics(ctx context.Context, allocID, containerName string) (string, error)
}

// Config holds the configuration for the container manager
type Config struct {
	// Backend specifies which container runtime to use ("docker" or "containerd")
	Backend string `json:"backend"`

	// Docker hosts - list of DOCKER_HOST values for remote Docker daemons
	// For containerd, these are CONTAINERD_ADDRESS values
	// Empty string means local daemon
	DockerHosts []string `json:"docker_hosts"`

	// Default resource limits
	DefaultCPURequest    string `json:"default_cpu_request"`
	DefaultMemoryRequest string `json:"default_memory_request"`
	DefaultStorageSize   string `json:"default_storage_size"`
}

// DefaultConfig returns a sensible default configuration
func DefaultConfig() *Config {
	return &Config{
		Backend:              "docker", // Default to Docker for backward compatibility
		DockerHosts:          []string{""}, // Default to local daemon
		DefaultCPURequest:    "100m",
		DefaultMemoryRequest: "256Mi",
		DefaultStorageSize:   "1Gi",
	}
}

// validateConfig ensures all required fields are present
func validateConfig(cfg *Config) error {
	if cfg.DockerHosts == nil || len(cfg.DockerHosts) == 0 {
		return fmt.Errorf("at least one host is required")
	}
	if cfg.Backend == "" {
		cfg.Backend = "docker" // Default to Docker
	}
	if cfg.Backend != "docker" && cfg.Backend != "containerd" {
		return fmt.Errorf("invalid backend: %s (must be 'docker' or 'containerd')", cfg.Backend)
	}
	return nil
}

// NewManager creates a new container manager based on the configured backend
func NewManager(cfg *Config) (Manager, error) {
	if err := validateConfig(cfg); err != nil {
		return nil, err
	}

	switch cfg.Backend {
	case "docker":
		return NewDockerManager(cfg)
	case "containerd":
		// Use nerdctl for containerd backend (proper networking support)
		return NewNerdctlManager(cfg)
	default:
		return nil, fmt.Errorf("unsupported backend: %s", cfg.Backend)
	}
}
