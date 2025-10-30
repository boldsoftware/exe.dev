// Package container provides container management for exe.dev
package container

import (
	"time"
)

// ContainerStatus represents the current state of a container
type ContainerStatus string

const (
	StatusPending  ContainerStatus = "pending"
	StatusRunning  ContainerStatus = "running"
	StatusStopped  ContainerStatus = "stopped"
	StatusFailed   ContainerStatus = "failed"
	StatusBuilding ContainerStatus = "building"
	StatusUnknown  ContainerStatus = "unknown"
)

func (cs ContainerStatus) String() string {
	return string(cs)
}

// CreateProgress represents the phase of container creation
type CreateProgress int

const (
	CreateInit  CreateProgress = iota // nothing has happened yet
	CreatePull                        // getting the image, when this is set the image bytes should be set
	CreateStart                       // the image has been pulled the container is about to be started
	CreateSSH                         // the container has started, now setting up ssh
	CreateDone                        // process complete
)

// CreateProgressInfo contains detailed progress information for container creation
type CreateProgressInfo struct {
	Phase           CreateProgress
	ImageBytes      int64  // Total size of the image
	DownloadedBytes int64  // Bytes downloaded so far (only valid during CreatePull)
	Message         string // Optional status message
}

// Container represents a container instance within an allocation
type Container struct {
	ID        string          `json:"id"`
	AllocID   string          `json:"alloc_id"`
	Name      string          `json:"name"`
	Image     string          `json:"image"`
	Status    ContainerStatus `json:"status"`
	CreatedAt time.Time       `json:"created_at"`
	StartedAt *time.Time      `json:"started_at,omitempty"`
	StoppedAt *time.Time      `json:"stopped_at,omitempty"`

	// Container backend fields
	Namespace  string `json:"namespace"`
	PodName    string `json:"pod_name"`
	PVCName    string `json:"pvc_name"`
	DockerHost string `json:"docker_host,omitempty"` // Deprecated: Container host URL (kept for backward compatibility)

	// Configuration
	CPURequest    string `json:"cpu_request"`
	MemoryRequest string `json:"memory_request"`
	StorageSize   string `json:"storage_size"`

	// Custom build information
	HasCustomImage bool   `json:"has_custom_image"`
	BuildID        string `json:"build_id,omitempty"`

	// SSH key material for container access
	SSHServerIdentityKey string `json:"-"`                  // SSH server private key (PEM) - not exposed in JSON
	SSHAuthorizedKeys    string `json:"-"`                  // User certificate for authorized_keys - not exposed in JSON
	SSHClientPrivateKey  string `json:"-"`                  // Private key for connecting to container - not exposed in JSON
	SSHPort              int    `json:"ssh_port,omitempty"` // SSH port exposed for this container
	SSHUser              string `json:"ssh_user,omitempty"` // User to connect as (from Docker image USER directive)

	// Exposed ports from image config for routing decisions
	ExposedPorts map[string]struct{} `json:"exposed_ports,omitempty"`
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
	AllocID string `json:"alloc_id"` // Allocation ID for this container
	Name    string `json:"name"`
	BoxID   int    `json:"box_id"`          // Box ID for persistent disk path
	Image   string `json:"image,omitempty"` // Optional, defaults to "ubuntu"
	Host    string `json:"host,omitempty"`  // Target container host (required)

	// Resource configuration
	Size          string `json:"size,omitempty"`           // T-shirt size: micro, small, medium, large, xlarge
	CPURequest    string `json:"cpu_request,omitempty"`    // Set by size
	MemoryRequest string `json:"memory_request,omitempty"` // Set by size
	StorageSize   string `json:"storage_size,omitempty"`   // Can be overridden with --disk

	// Command override: "auto" (default), "none", or custom command string
	CommandOverride string `json:"command_override,omitempty"`

	// ProgressCallback - callback with detailed progress information
	ProgressCallback func(info CreateProgressInfo) `json:"-"`

	// ExistingSSHKeys - when recreating a container, pass the existing SSH keys
	// to preserve host key continuity
	ExistingSSHKeys *ContainerSSHKeys `json:"-"`
}

// BuildRequest represents a request to build a custom Docker image
type BuildRequest struct {
	AllocID           string `json:"alloc_id"`
	Dockerfile        string `json:"dockerfile"`
	DockerfileContent string `json:"dockerfile_content"`
	BuildID           string `json:"build_id"`
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
	Container *Container `json:"container"`
	LocalPort int        `json:"local_port"`
	StopFunc  func()     `json:"-"` // Function to stop the port-forward
}
