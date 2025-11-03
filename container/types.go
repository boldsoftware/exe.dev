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

// Default resource requests for new containers. Equivalent to the old "medium" size.
const (
	DefaultCPURequest    = "1000m"
	DefaultMemoryRequest = "4Gi"
	DefaultStorageSize   = "20Gi"
)

// CreateContainerRequest represents the parameters for creating a new container
type CreateContainerRequest struct {
	AllocID string `json:"alloc_id"` // Allocation ID for this container
	Name    string `json:"name"`
	BoxID   int    `json:"box_id"`          // Box ID for persistent disk path
	Image   string `json:"image,omitempty"` // Optional, defaults to "ubuntu"
	Host    string `json:"host,omitempty"`  // Target container host (required)

	// Resource configuration
	CPURequest    string `json:"cpu_request,omitempty"`    // Defaulted to medium profile if empty
	MemoryRequest string `json:"memory_request,omitempty"` // Defaulted to medium profile if empty
	StorageSize   string `json:"storage_size,omitempty"`   // Can be overridden with --disk

	// Command override: "auto" (default), "none", or custom command string
	CommandOverride string `json:"command_override,omitempty"`

	// ProgressCallback - callback with detailed progress information
	ProgressCallback func(info CreateProgressInfo) `json:"-"`

	// ExistingSSHKeys - when recreating a container, pass the existing SSH keys
	// to preserve host key continuity
	ExistingSSHKeys *ContainerSSHKeys `json:"-"`
}
