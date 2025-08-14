package container

import (
	"context"
	"io"
)

// Backend represents a container orchestration backend (Docker, etc.)
type Backend interface {
	// CreateContainer creates a new container with the given configuration
	CreateContainer(ctx context.Context, config *CreateConfig) (*Container, error)

	// GetContainer retrieves container information
	GetContainer(ctx context.Context, containerID string) (*Container, error)

	// ListContainers lists all containers for a user
	ListContainers(ctx context.Context, userID string) ([]*Container, error)

	// StartContainer starts a stopped container
	StartContainer(ctx context.Context, containerID string) error

	// StopContainer stops a running container
	StopContainer(ctx context.Context, containerID string) error

	// DeleteContainer deletes a container and its resources
	DeleteContainer(ctx context.Context, containerID string) error

	// ExecuteInContainer executes a command in a container
	ExecuteInContainer(ctx context.Context, containerID string, command []string, stdin io.Reader) (stdout, stderr string, err error)

	// GetContainerLogs retrieves container logs
	GetContainerLogs(ctx context.Context, containerID string, lines int) (string, error)

	// ConnectToContainer establishes an SSH-like connection to a container
	ConnectToContainer(ctx context.Context, containerID string) (Connection, error)

	// WakeContainer wakes a sleeping container
	WakeContainer(ctx context.Context, containerID string) error

	// GetBackendType returns the backend type (e.g., "docker")
	GetBackendType() string
}

// CreateConfig contains configuration for creating a new container
type CreateConfig struct {
	UserID      string
	Name        string
	TeamName    string
	Image       string
	Size        string
	Dockerfile  string
	Ephemeral   bool
	Environment map[string]string
}

// Connection represents a connection to a container
type Connection interface {
	io.ReadWriteCloser

	// Resize resizes the terminal
	Resize(width, height int) error

	// ExecuteCommand executes a command and returns output
	ExecuteCommand(command []string) (stdout, stderr string, err error)
}
