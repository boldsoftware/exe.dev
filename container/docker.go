package container

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/creack/pty"
)

// DockerManager implements the Manager interface using Docker with DOCKER_HOST support
type DockerManager struct {
	config *Config
	hosts  []string // List of DOCKER_HOST values

	mu         sync.RWMutex
	containers map[string]*Container // containerID -> Container
}

// NewDockerManager creates a new Docker-based container manager
func NewDockerManager(config *Config) (*DockerManager, error) {
	if err := validateConfig(config); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	manager := &DockerManager{
		config:     config,
		hosts:      config.DockerHosts,
		containers: make(map[string]*Container),
	}

	// Discover existing containers on all hosts with timeout
	for _, host := range config.DockerHosts {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		if err := manager.discoverContainers(ctx, host); err != nil {
			log.Printf("Warning: Failed to discover containers on host %s: %v", host, err)
		}
		cancel()
	}

	return manager, nil
}

// discoverContainers discovers existing containers on a Docker host
func (m *DockerManager) discoverContainers(ctx context.Context, dockerHost string) error {
	cmd := exec.CommandContext(ctx, "docker", "ps", "-a", "--format", "json")
	if dockerHost != "" {
		cmd.Env = append(os.Environ(), fmt.Sprintf("DOCKER_HOST=%s", dockerHost))
	}

	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("failed to list containers: %w", err)
	}

	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}

		var containerInfo struct {
			ID      string `json:"ID"`
			Names   string `json:"Names"`
			Image   string `json:"Image"`
			State   string `json:"State"`
			Created int64  `json:"CreatedAt"`
			Labels  string `json:"Labels"`
		}

		if err := json.Unmarshal([]byte(line), &containerInfo); err != nil {
			continue
		}

		// Only track containers we manage (with exe- prefix)
		if !strings.HasPrefix(containerInfo.Names, "exe-") {
			continue
		}

		// Parse labels to extract metadata
		labels := parseLabels(containerInfo.Labels)

		container := &Container{
			ID:         containerInfo.ID,
			Name:       strings.TrimPrefix(containerInfo.Names, "exe-"),
			UserID:     labels["user_id"],
			TeamName:   labels["team"],
			Status:     mapDockerStatus(containerInfo.State),
			Image:      containerInfo.Image,
			CreatedAt:  time.Unix(containerInfo.Created, 0),
			PodName:    containerInfo.Names,
			Namespace:  "docker",
			DockerHost: dockerHost,
		}

		m.mu.Lock()
		m.containers[container.ID] = container
		m.mu.Unlock()
	}

	return nil
}

// CreateContainer creates a new Docker container
func (m *DockerManager) CreateContainer(ctx context.Context, req *CreateContainerRequest) (*Container, error) {
	// Select a Docker host (round-robin or least loaded)
	dockerHost := m.selectHost()

	// Generate container name
	containerName := fmt.Sprintf("exe-%s-%s", req.TeamName, req.Name)

	// Build Docker run command
	args := []string{
		"run", "-d",
		"--name", containerName,
		"--label", fmt.Sprintf("user_id=%s", req.UserID),
		"--label", fmt.Sprintf("team=%s", req.TeamName),
		"--label", "managed_by=exe",
	}

	// Add resource limits if specified
	if m.config.DefaultCPURequest != "" {
		// Convert Kubernetes-style CPU request to Docker format
		// e.g., "100m" -> "0.1"
		cpuLimit := convertCPULimit(m.config.DefaultCPURequest)
		args = append(args, "--cpus", cpuLimit)
	}

	if m.config.DefaultMemoryRequest != "" {
		// e.g., "256Mi" -> "256m"
		memLimit := convertMemoryLimit(m.config.DefaultMemoryRequest)
		args = append(args, "--memory", memLimit)
	}

	// Mount a volume for persistent storage
	volumeName := fmt.Sprintf("exe-vol-%s-%s", req.TeamName, req.Name)
	args = append(args, "-v", fmt.Sprintf("%s:/workspace", volumeName))

	// Set working directory
	args = append(args, "-w", "/workspace")

	// Add the image and command
	image := req.Image
	if image == "" {
		image = "ubuntu:latest"
	}
	args = append(args, image)

	// Keep container running
	args = append(args, "tail", "-f", "/dev/null")

	// Execute docker run
	cmd := exec.CommandContext(ctx, "docker", args...)
	if dockerHost != "" {
		cmd.Env = append(os.Environ(), fmt.Sprintf("DOCKER_HOST=%s", dockerHost))
	}

	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("failed to create container: %w: %s", err, output)
	}

	containerID := strings.TrimSpace(string(output))

	// Get container info
	container := &Container{
		ID:         containerID,
		Name:       req.Name,
		UserID:     req.UserID,
		TeamName:   req.TeamName,
		Status:     StatusRunning,
		Image:      image,
		CreatedAt:  time.Now(),
		PodName:    containerName,
		Namespace:  "docker",
		DockerHost: dockerHost,
	}

	m.mu.Lock()
	m.containers[containerID] = container
	m.mu.Unlock()

	return container, nil
}

// GetContainer retrieves container information
func (m *DockerManager) GetContainer(ctx context.Context, userID, containerID string) (*Container, error) {
	m.mu.RLock()
	container, exists := m.containers[containerID]
	m.mu.RUnlock()

	if !exists {
		return nil, fmt.Errorf("container not found: %s", containerID)
	}

	// Verify user owns this container
	if container.UserID != userID {
		return nil, fmt.Errorf("container not owned by user")
	}

	// Update status from Docker
	if err := m.updateContainerStatus(ctx, container); err != nil {
		log.Printf("Warning: Failed to update container status: %v", err)
	}

	return container, nil
}

// ListContainers lists all containers for a user
func (m *DockerManager) ListContainers(ctx context.Context, userID string) ([]*Container, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var containers []*Container
	for _, container := range m.containers {
		if container.UserID == userID {
			// Update status
			if err := m.updateContainerStatus(ctx, container); err != nil {
				log.Printf("Warning: Failed to update container status: %v", err)
			}
			containers = append(containers, container)
		}
	}

	return containers, nil
}

// StartContainer starts a stopped container
func (m *DockerManager) StartContainer(ctx context.Context, userID, containerID string) error {
	container, err := m.GetContainer(ctx, userID, containerID)
	if err != nil {
		return err
	}

	cmd := exec.CommandContext(ctx, "docker", "start", container.PodName)
	if container.DockerHost != "" {
		cmd.Env = append(os.Environ(), fmt.Sprintf("DOCKER_HOST=%s", container.DockerHost))
	}

	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to start container: %w: %s", err, output)
	}

	container.Status = StatusRunning
	return nil
}

// StopContainer stops a running container
func (m *DockerManager) StopContainer(ctx context.Context, userID, containerID string) error {
	container, err := m.GetContainer(ctx, userID, containerID)
	if err != nil {
		return err
	}

	cmd := exec.CommandContext(ctx, "docker", "stop", container.PodName)
	if container.DockerHost != "" {
		cmd.Env = append(os.Environ(), fmt.Sprintf("DOCKER_HOST=%s", container.DockerHost))
	}

	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to stop container: %w: %s", err, output)
	}

	container.Status = StatusStopped
	return nil
}

// DeleteContainer deletes a container and its resources
func (m *DockerManager) DeleteContainer(ctx context.Context, userID, containerID string) error {
	container, err := m.GetContainer(ctx, userID, containerID)
	if err != nil {
		return err
	}

	// Remove container (using -f to force remove even if running)
	// No need to stop first since -f will handle that
	cmd := exec.CommandContext(ctx, "docker", "rm", "-f", container.PodName)
	if container.DockerHost != "" {
		cmd.Env = append(os.Environ(), fmt.Sprintf("DOCKER_HOST=%s", container.DockerHost))
	}

	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to delete container: %w: %s", err, output)
	}

	// Remove volume
	volumeName := fmt.Sprintf("exe-vol-%s-%s", container.TeamName, container.Name)
	cmd = exec.CommandContext(ctx, "docker", "volume", "rm", volumeName)
	if container.DockerHost != "" {
		cmd.Env = append(os.Environ(), fmt.Sprintf("DOCKER_HOST=%s", container.DockerHost))
	}
	_ = cmd.Run() // Ignore error if volume doesn't exist

	m.mu.Lock()
	delete(m.containers, containerID)
	m.mu.Unlock()

	return nil
}

// BuildImage builds a Docker image
func (m *DockerManager) BuildImage(ctx context.Context, req *BuildRequest) (*BuildResult, error) {
	// For Docker, we build locally
	dockerHost := m.selectHost()

	// Create a temporary directory for the build context
	tmpDir, err := os.MkdirTemp("", "exe-build-")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	// Write Dockerfile
	dockerfilePath := fmt.Sprintf("%s/Dockerfile", tmpDir)
	if err := os.WriteFile(dockerfilePath, []byte(req.DockerfileContent), 0644); err != nil {
		return nil, fmt.Errorf("failed to write Dockerfile: %w", err)
	}

	// Generate image name
	imageName := fmt.Sprintf("exe-custom-%s-%d", req.TeamName, time.Now().Unix())

	// Build the image
	cmd := exec.CommandContext(ctx, "docker", "build", "-t", imageName, tmpDir)
	if dockerHost != "" {
		cmd.Env = append(os.Environ(), fmt.Sprintf("DOCKER_HOST=%s", dockerHost))
	}

	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("failed to build image: %w: %s", err, output)
	}

	return &BuildResult{
		BuildID:   fmt.Sprintf("build-%d", time.Now().Unix()),
		ImageName: imageName,
		Status:    "SUCCESS",
		LogsURL:   "local",
	}, nil
}

// GetBuildStatus returns the status of a build
func (m *DockerManager) GetBuildStatus(ctx context.Context, buildID string) (*BuildResult, error) {
	// For Docker, builds are synchronous
	return &BuildResult{
		BuildID: buildID,
		Status:  "SUCCESS",
	}, nil
}

// GetContainerLogs retrieves container logs
func (m *DockerManager) GetContainerLogs(ctx context.Context, userID, containerID string, lines int) ([]string, error) {
	container, err := m.GetContainer(ctx, userID, containerID)
	if err != nil {
		return nil, err
	}

	args := []string{"logs"}
	if lines > 0 {
		args = append(args, "--tail", fmt.Sprintf("%d", lines))
	}
	args = append(args, container.PodName)

	cmd := exec.CommandContext(ctx, "docker", args...)
	if container.DockerHost != "" {
		cmd.Env = append(os.Environ(), fmt.Sprintf("DOCKER_HOST=%s", container.DockerHost))
	}

	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to get logs: %w", err)
	}

	return strings.Split(string(output), "\n"), nil
}

// ConnectToContainer establishes a connection to a container
func (m *DockerManager) ConnectToContainer(ctx context.Context, userID, containerID string) (*ContainerConnection, error) {
	container, err := m.GetContainer(ctx, userID, containerID)
	if err != nil {
		return nil, err
	}

	return &ContainerConnection{
		Container: container,
		LocalPort: 0,         // No port forwarding needed for Docker
		StopFunc:  func() {}, // No-op
	}, nil
}

// ExecuteInContainer executes a command in a container
func (m *DockerManager) ExecuteInContainer(ctx context.Context, userID, containerID string, cmd []string, stdin io.Reader, stdout, stderr io.Writer) error {
	container, err := m.GetContainer(ctx, userID, containerID)
	if err != nil {
		return err
	}

	// Build docker exec command with -t flag for PTY allocation
	args := []string{"exec", "-it"}
	args = append(args, container.PodName)
	args = append(args, cmd...)

	execCmd := exec.CommandContext(ctx, "docker", args...)
	if container.DockerHost != "" {
		execCmd.Env = append(os.Environ(), fmt.Sprintf("DOCKER_HOST=%s", container.DockerHost))
	}

	// For interactive sessions with both stdin and stdout, use PTY
	// This handles SSH sessions properly
	if stdin != nil && stdout != nil {
		// Start the command with a pseudo-terminal
		ptmx, err := pty.Start(execCmd)
		if err != nil {
			return fmt.Errorf("failed to start with pty: %w", err)
		}
		defer ptmx.Close()

		// Set up bidirectional copying
		done := make(chan error, 2)

		// Copy stdin to pty
		go func() {
			_, err := io.Copy(ptmx, stdin)
			done <- err
		}()

		// Copy pty to stdout (this also handles stderr since PTY combines them)
		go func() {
			_, err := io.Copy(stdout, ptmx)
			done <- err
		}()

		// Wait for command to finish
		cmdErr := execCmd.Wait()

		// Close the PTY to signal EOF to the copy goroutines
		ptmx.Close()

		// Wait for at least one copy to finish (usually the stdout copy)
		<-done

		return cmdErr
	}

	// For non-interactive commands, run without PTY
	// Only use -i flag if we have stdin
	args = []string{"exec"}
	if stdin != nil {
		args = append(args, "-i")
	}
	args = append(args, container.PodName)
	args = append(args, cmd...)

	execCmd = exec.CommandContext(ctx, "docker", args...)
	if container.DockerHost != "" {
		execCmd.Env = append(os.Environ(), fmt.Sprintf("DOCKER_HOST=%s", container.DockerHost))
	}

	execCmd.Stdin = stdin
	execCmd.Stdout = stdout
	execCmd.Stderr = stderr
	return execCmd.Run()
}

// Close cleans up resources
func (m *DockerManager) Close() error {
	// Nothing to clean up for Docker
	return nil
}

// GetContainerDiagnostics returns diagnostic information about a container
func (m *DockerManager) GetContainerDiagnostics(ctx context.Context, userID, containerName string) (string, error) {
	containers, err := m.ListContainers(ctx, userID)
	if err != nil {
		return "", err
	}

	for _, c := range containers {
		if c.Name == containerName {
			var buf bytes.Buffer
			fmt.Fprintf(&buf, "Container: %s\n", c.Name)
			fmt.Fprintf(&buf, "ID: %s\n", c.ID)
			fmt.Fprintf(&buf, "Status: %s\n", c.Status)
			fmt.Fprintf(&buf, "Image: %s\n", c.Image)
			fmt.Fprintf(&buf, "Created: %v\n", c.CreatedAt)
			fmt.Fprintf(&buf, "Docker Host: %s\n", c.DockerHost)
			fmt.Fprintf(&buf, "Backend: Docker\n")

			// Get more details from Docker
			cmd := exec.CommandContext(ctx, "docker", "inspect", c.PodName)
			if c.DockerHost != "" {
				cmd.Env = append(os.Environ(), fmt.Sprintf("DOCKER_HOST=%s", c.DockerHost))
			}

			if output, err := cmd.Output(); err == nil {
				var inspectData []map[string]interface{}
				if err := json.Unmarshal(output, &inspectData); err == nil && len(inspectData) > 0 {
					if state, ok := inspectData[0]["State"].(map[string]interface{}); ok {
						fmt.Fprintf(&buf, "\nDocker State:\n")
						fmt.Fprintf(&buf, "  Running: %v\n", state["Running"])
						fmt.Fprintf(&buf, "  Status: %v\n", state["Status"])
						if startedAt, ok := state["StartedAt"].(string); ok {
							fmt.Fprintf(&buf, "  Started: %s\n", startedAt)
						}
					}
				}
			}

			return buf.String(), nil
		}
	}

	return "", fmt.Errorf("container %s not found", containerName)
}

// Helper functions

func (m *DockerManager) selectHost() string {
	// Simple round-robin or return first host
	// In production, this could be more sophisticated (least loaded, etc.)
	if len(m.hosts) == 0 {
		return ""
	}
	return m.hosts[0]
}

func (m *DockerManager) updateContainerStatus(ctx context.Context, container *Container) error {
	cmd := exec.CommandContext(ctx, "docker", "inspect", container.PodName, "--format", "{{.State.Status}}")
	if container.DockerHost != "" {
		cmd.Env = append(os.Environ(), fmt.Sprintf("DOCKER_HOST=%s", container.DockerHost))
	}

	output, err := cmd.Output()
	if err != nil {
		return err
	}

	status := strings.TrimSpace(string(output))
	container.Status = mapDockerStatus(status)
	return nil
}

func parseLabels(labelStr string) map[string]string {
	labels := make(map[string]string)
	for _, label := range strings.Split(labelStr, ",") {
		parts := strings.SplitN(label, "=", 2)
		if len(parts) == 2 {
			labels[parts[0]] = parts[1]
		}
	}
	return labels
}

func mapDockerStatus(dockerStatus string) ContainerStatus {
	switch dockerStatus {
	case "running":
		return StatusRunning
	case "exited", "dead":
		return StatusStopped
	case "paused":
		return StatusPending
	case "created":
		return StatusPending
	default:
		return StatusUnknown
	}
}

func convertCPULimit(k8sFormat string) string {
	// Convert Kubernetes CPU format to Docker format
	// e.g., "100m" -> "0.1", "1" -> "1"
	if strings.HasSuffix(k8sFormat, "m") {
		millis := strings.TrimSuffix(k8sFormat, "m")
		if val, err := fmt.Sscanf(millis, "%d", new(int)); err == nil && val == 1 {
			var m int
			fmt.Sscanf(millis, "%d", &m)
			return fmt.Sprintf("%.2f", float64(m)/1000.0)
		}
	}
	return k8sFormat
}

func convertMemoryLimit(k8sFormat string) string {
	// Convert Kubernetes memory format to Docker format
	// e.g., "256Mi" -> "256m", "1Gi" -> "1g"
	replacements := map[string]string{
		"Mi": "m",
		"Gi": "g",
		"Ki": "k",
	}

	result := k8sFormat
	for k, v := range replacements {
		if strings.HasSuffix(result, k) {
			result = strings.TrimSuffix(result, k) + v
			break
		}
	}
	return result
}
