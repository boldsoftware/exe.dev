package container

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"
)

// DockerManager wraps DockerBackend to implement the full Manager interface
type DockerManager struct {
	backend *DockerBackend
}

// NewDockerManager creates a new Docker-based container manager for local development
func NewDockerManager() Manager {
	return &DockerManager{
		backend: NewDockerBackend(),
	}
}

// CreateContainer creates a new Docker container
func (m *DockerManager) CreateContainer(ctx context.Context, req *CreateContainerRequest) (*Container, error) {
	config := &CreateConfig{
		TeamName: req.TeamName,
		Name:     req.Name,
		UserID:   req.UserID,
		Image:    req.Image,
	}
	return m.backend.CreateContainer(ctx, config)
}

// GetContainer retrieves container information
func (m *DockerManager) GetContainer(ctx context.Context, userID, containerID string) (*Container, error) {
	return m.backend.GetContainer(ctx, containerID)
}

// ListContainers lists all containers for a user
func (m *DockerManager) ListContainers(ctx context.Context, userID string) ([]*Container, error) {
	return m.backend.ListContainers(ctx, userID)
}

// StartContainer starts a stopped container
func (m *DockerManager) StartContainer(ctx context.Context, userID, containerID string) error {
	return m.backend.StartContainer(ctx, containerID)
}

// StopContainer stops a running container
func (m *DockerManager) StopContainer(ctx context.Context, userID, containerID string) error {
	return m.backend.StopContainer(ctx, containerID)
}

// DeleteContainer deletes a container and its resources
func (m *DockerManager) DeleteContainer(ctx context.Context, userID, containerID string) error {
	return m.backend.DeleteContainer(ctx, containerID)
}

// BuildImage builds a Docker image locally
func (m *DockerManager) BuildImage(ctx context.Context, req *BuildRequest) (*BuildResult, error) {
	// For local Docker, we don't need Cloud Build - just return success
	// The actual image building happens when creating the container
	return &BuildResult{
		BuildID:   fmt.Sprintf("local-build-%d", time.Now().Unix()),
		ImageName: "local-image",
		Status:    "SUCCESS",
		LogsURL:   "local",
	}, nil
}

// GetBuildStatus returns the status of a build
func (m *DockerManager) GetBuildStatus(ctx context.Context, buildID string) (*BuildResult, error) {
	// For local Docker, builds are always instant
	return &BuildResult{
		BuildID: buildID,
		Status:  "SUCCESS",
	}, nil
}

// GetContainerLogs retrieves container logs
func (m *DockerManager) GetContainerLogs(ctx context.Context, userID, containerID string, lines int) ([]string, error) {
	logs, err := m.backend.GetContainerLogs(ctx, containerID, lines)
	if err != nil {
		return nil, err
	}
	return strings.Split(logs, "\n"), nil
}

// ConnectToContainer establishes a connection to a container
func (m *DockerManager) ConnectToContainer(ctx context.Context, userID, containerID string) (*ContainerConnection, error) {
	container, err := m.backend.GetContainer(ctx, containerID)
	if err != nil {
		return nil, err
	}
	
	// Return a ContainerConnection (for Docker, no port forwarding needed)
	return &ContainerConnection{
		Container: container,
		LocalPort: 0, // No port forwarding for local Docker
		StopFunc:  func() {}, // No-op stop function
	}, nil
}

// ExecuteInContainer executes a command in a container
func (m *DockerManager) ExecuteInContainer(ctx context.Context, userID, containerID string, cmd []string, stdin io.Reader, stdout, stderr io.Writer) error {
	// Check if this is an interactive shell command
	isInteractive := false
	if len(cmd) > 0 && (cmd[0] == "/bin/bash" || cmd[0] == "/bin/sh" || cmd[0] == "bash" || cmd[0] == "sh") {
		isInteractive = true
	}
	
	// For interactive shells, use streaming execution with PTY
	if isInteractive && stdin != nil && stdout != nil {
		return m.backend.ExecuteInContainerWithPTY(ctx, containerID, cmd, stdin, stdout, stderr)
	}
	
	// For non-interactive commands, use buffered execution
	stdoutStr, stderrStr, err := m.backend.ExecuteInContainer(ctx, containerID, cmd, stdin)
	
	if stdout != nil && stdoutStr != "" {
		stdout.Write([]byte(stdoutStr))
	}
	if stderr != nil && stderrStr != "" {
		stderr.Write([]byte(stderrStr))
	}
	
	return err
}

// Close cleans up resources
func (m *DockerManager) Close() error {
	// No cleanup needed for Docker backend
	return nil
}

// GetContainerDiagnostics returns diagnostic information about a container
func (m *DockerManager) GetContainerDiagnostics(ctx context.Context, userID, containerName string) (string, error) {
	containers, err := m.backend.ListContainers(ctx, userID)
	if err != nil {
		return "", err
	}
	
	for _, c := range containers {
		if c.Name == containerName {
			return fmt.Sprintf("Container: %s\nStatus: %s\nImage: %s\nCreated: %v\nBackend: Docker (local)",
				c.Name, c.Status, c.Image, c.CreatedAt), nil
		}
	}
	
	return "", fmt.Errorf("container %s not found", containerName)
}