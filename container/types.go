// Package container provides Google Cloud-based container management for exe.dev
package container

import (
	"time"
)

// ContainerStatus represents the current state of a container
type ContainerStatus string

const (
	StatusPending   ContainerStatus = "pending"
	StatusRunning   ContainerStatus = "running"
	StatusStopped   ContainerStatus = "stopped"
	StatusFailed    ContainerStatus = "failed"
	StatusBuilding  ContainerStatus = "building"
	StatusUnknown   ContainerStatus = "unknown"
)

// Container represents a user's container instance
type Container struct {
	ID          string          `json:"id"`
	UserID      string          `json:"user_id"`
	Name        string          `json:"name"`
	TeamName    string          `json:"team_name,omitempty"`
	Image       string          `json:"image"`
	Status      ContainerStatus `json:"status"`
	CreatedAt   time.Time       `json:"created_at"`
	StartedAt   *time.Time      `json:"started_at,omitempty"`
	StoppedAt   *time.Time      `json:"stopped_at,omitempty"`
	
	// GKE-specific fields
	Namespace   string `json:"namespace"`
	PodName     string `json:"pod_name"`
	PVCName     string `json:"pvc_name"`
	
	// Configuration
	CPURequest    string `json:"cpu_request"`
	MemoryRequest string `json:"memory_request"`
	StorageSize   string `json:"storage_size"`
	
	// Custom build information
	HasCustomImage bool   `json:"has_custom_image"`
	BuildID        string `json:"build_id,omitempty"`
}

// ContainerSize represents a t-shirt size preset for containers
type ContainerSize struct {
	Name          string
	DisplayName   string
	CPURequest    string
	MemoryRequest string
	StorageSize   string
	Description   string
}

// Available container sizes
var ContainerSizes = map[string]ContainerSize{
	"micro": {
		Name:          "micro",
		DisplayName:   "Micro",
		CPURequest:    "250m",
		MemoryRequest: "512Mi",
		StorageSize:   "5Gi",
		Description:   "0.25 CPU, 512MB RAM, 5GB disk",
	},
	"small": {
		Name:          "small",
		DisplayName:   "Small",
		CPURequest:    "500m",
		MemoryRequest: "2Gi",
		StorageSize:   "10Gi",
		Description:   "0.5 CPU, 2GB RAM, 10GB disk",
	},
	"medium": {
		Name:          "medium",
		DisplayName:   "Medium",
		CPURequest:    "1000m",
		MemoryRequest: "4Gi",
		StorageSize:   "20Gi",
		Description:   "1 CPU, 4GB RAM, 20GB disk",
	},
	"large": {
		Name:          "large",
		DisplayName:   "Large",
		CPURequest:    "2000m",
		MemoryRequest: "8Gi",
		StorageSize:   "50Gi",
		Description:   "2 CPU, 8GB RAM, 50GB disk",
	},
	"xlarge": {
		Name:          "xlarge",
		DisplayName:   "XLarge",
		CPURequest:    "4000m",
		MemoryRequest: "16Gi",
		StorageSize:   "100Gi",
		Description:   "4 CPU, 16GB RAM, 100GB disk",
	},
}

// CreateContainerRequest represents the parameters for creating a new container
type CreateContainerRequest struct {
	UserID      string `json:"user_id"`
	Name        string `json:"name"`
	TeamName    string `json:"team_name,omitempty"` // Team name for hostname configuration
	Image       string `json:"image,omitempty"` // Optional, defaults to "ubuntu"
	Dockerfile  string `json:"dockerfile,omitempty"` // Optional custom Dockerfile
	
	// Resource configuration
	Size        string `json:"size,omitempty"`         // T-shirt size: micro, small, medium, large, xlarge
	CPURequest    string `json:"cpu_request,omitempty"`    // Set by size
	MemoryRequest string `json:"memory_request,omitempty"` // Set by size
	StorageSize   string `json:"storage_size,omitempty"`   // Can be overridden with --disk
	
	// Ephemeral flag - if true, no PVC is created
	Ephemeral   bool   `json:"ephemeral,omitempty"`
	
	// Sandbox configuration - allow opting out of sandbox for specific containers
	DisableSandbox bool `json:"disable_sandbox,omitempty"`
}

// BuildRequest represents a request to build a custom Docker image
type BuildRequest struct {
	UserID     string `json:"user_id"`
	Dockerfile string `json:"dockerfile"`
	BuildID    string `json:"build_id"`
}

// BuildResult contains the result of a Docker image build
type BuildResult struct {
	BuildID   string `json:"build_id"`
	ImageName string `json:"image_name"`
	Status    string `json:"status"`
	LogsURL   string `json:"logs_url,omitempty"`
	Error     string `json:"error,omitempty"`
}

// ContainerConnection represents an active connection to a container
type ContainerConnection struct {
	Container  *Container `json:"container"`
	LocalPort  int        `json:"local_port"`
	StopFunc   func()     `json:"-"` // Function to stop the port-forward
}