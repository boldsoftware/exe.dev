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
}

// Config holds the configuration for the container manager
type Config struct {
	// Google Cloud configuration
	ProjectID       string `json:"project_id"`
	Region          string `json:"region"`
	ClusterName     string `json:"cluster_name"`
	ClusterLocation string `json:"cluster_location"`
	
	// Container Registry configuration  
	RegistryHost string `json:"registry_host"` // e.g., "gcr.io" or "us-docker.pkg.dev"
	
	// Default resource limits
	DefaultCPURequest    string `json:"default_cpu_request"`
	DefaultMemoryRequest string `json:"default_memory_request"`
	DefaultStorageSize   string `json:"default_storage_size"`
	
	// Namespace configuration
	NamespacePrefix string `json:"namespace_prefix"` // e.g., "exe-"
}

// DefaultConfig returns a sensible default configuration
func DefaultConfig(projectID string) *Config {
	return &Config{
		ProjectID:            projectID,
		Region:               "us-central1",
		ClusterName:          "exe-autopilot",
		ClusterLocation:      "us-central1",
		RegistryHost:         "gcr.io",
		DefaultCPURequest:    "100m",
		DefaultMemoryRequest: "256Mi", 
		DefaultStorageSize:   "10Gi",
		NamespacePrefix:      "exe-",
	}
}

// validateConfig ensures all required fields are present
func validateConfig(cfg *Config) error {
	if cfg.ProjectID == "" {
		return fmt.Errorf("project_id is required")
	}
	if cfg.ClusterName == "" {
		return fmt.Errorf("cluster_name is required")
	}
	if cfg.ClusterLocation == "" {
		return fmt.Errorf("cluster_location is required")
	}
	return nil
}