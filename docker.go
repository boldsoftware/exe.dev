package exe

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// GetContainerHostPort gets the host port mapped to the container's SSH port
// TODO: This information should be stored in the database when the container is created,
// rather than querying Docker every time. The host and port should be in the machines table.
func (s *Server) GetContainerHostPort(containerID, allocID string) (string, int, error) {
	if s.containerManager == nil {
		return "", 0, fmt.Errorf("container manager not available")
	}

	// Get container details
	container, err := s.containerManager.GetContainer(context.Background(), allocID, containerID)
	if err != nil {
		return "", 0, fmt.Errorf("failed to get container: %v", err)
	}

	// Get the host port mapped to container port 22
	cmd := exec.Command("docker", "port", container.PodName, "22")
	if container.DockerHost != "" {
		cmd.Env = append(os.Environ(), fmt.Sprintf("DOCKER_HOST=%s", container.DockerHost))
	}

	output, err := cmd.Output()
	if err != nil {
		return "", 0, fmt.Errorf("failed to get container SSH port: %v", err)
	}

	// Parse port output: "0.0.0.0:32768\n[::]:32768" -> prefer IPv4
	portStr := strings.TrimSpace(string(output))
	if portStr == "" {
		return "", 0, fmt.Errorf("no SSH port mapping found for container")
	}

	// Handle multiple lines (IPv4 and IPv6) - prefer the first line
	lines := strings.Split(portStr, "\n")
	firstLine := strings.TrimSpace(lines[0])

	// Split "0.0.0.0:32768" into host and port
	parts := strings.Split(firstLine, ":")
	if len(parts) != 2 {
		return "", 0, fmt.Errorf("invalid port format: %s", firstLine)
	}

	host := parts[0]
	if host == "0.0.0.0" || host == "::" {
		// Map to localhost when bound to all interfaces
		host = "127.0.0.1"
	}

	port, err := strconv.Atoi(parts[1])
	if err != nil {
		return "", 0, fmt.Errorf("invalid port number: %s", parts[1])
	}

	return host, port, nil
}
