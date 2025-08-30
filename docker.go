package exe

import (
	"context"
	"fmt"
	"strings"
)

// GetContainerHostPort gets the host port mapped to the container's SSH port
func (s *Server) GetContainerHostPort(containerID, allocID string) (string, int, error) {
	if s.containerManager == nil {
		return "", 0, fmt.Errorf("container manager not available")
	}

	// Get container details
	container, err := s.containerManager.GetContainer(context.Background(), allocID, containerID)
	if err != nil {
		return "", 0, fmt.Errorf("failed to get container: %v", err)
	}

	// Use the SSHPort field that was set during container creation
	if container.SSHPort == 0 {
		return "", 0, fmt.Errorf("no SSH port configured for container")
	}

	// Extract hostname from DockerHost/containerd address if present
	host := "localhost"
	if container.DockerHost != "" {
		// DockerHost might be "tcp://hostname:port" or "ssh://hostname"
		if strings.HasPrefix(container.DockerHost, "tcp://") {
			hostPort := strings.TrimPrefix(container.DockerHost, "tcp://")
			parts := strings.Split(hostPort, ":")
			if len(parts) > 0 {
				host = parts[0]
			}
		} else if strings.HasPrefix(container.DockerHost, "ssh://") {
			host = strings.TrimPrefix(container.DockerHost, "ssh://")
		} else {
			// Plain hostname
			host = container.DockerHost
		}
	}

	return host, container.SSHPort, nil
}
