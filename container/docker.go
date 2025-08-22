package container

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/creack/pty"
	"golang.org/x/crypto/ssh"
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
	// Generate SSH keys for this container
	sshKeys, err := GenerateContainerSSHKeys()
	if err != nil {
		return nil, fmt.Errorf("failed to generate SSH keys: %w", err)
	}

	// Select a Docker host (round-robin or least loaded)
	dockerHost := m.selectHost()

	// Generate container name
	containerName := fmt.Sprintf("exe-%s-%s", req.TeamName, req.Name)

	// Build Docker run command
	args := []string{
		"run", "-d",
		"--name", containerName,
		"--hostname", fmt.Sprintf("%s.%s.exe.dev", req.Name, req.TeamName),
		"--label", fmt.Sprintf("user_id=%s", req.UserID),
		"--label", fmt.Sprintf("team=%s", req.TeamName),
		"--label", "managed_by=exe",
		"-p", "0:22", // Expose SSH port 22 to a random host port
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
	image = ExpandImageName(image)
	args = append(args, image)

	// Keep container running - SSH will be setup after container starts
	// TODO(philip): socat TCP-LISTEN:22,reuseaddr,fork EXEC:"/usr/sbin/sshd -i"
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

	// Wait for container to be running before proceeding
	// This avoids race conditions with port mapping and SSH setup
	// Use 30 seconds timeout for slower CI environments
	waitStart := time.Now()
	for {
		statusCmd := exec.CommandContext(ctx, "docker", "inspect", containerID, "--format", "{{.State.Status}}")
		if dockerHost != "" {
			statusCmd.Env = append(os.Environ(), fmt.Sprintf("DOCKER_HOST=%s", dockerHost))
		}
		statusOutput, err := statusCmd.Output()
		if err != nil {
			// Container might not be ready for inspect yet, continue waiting
			if time.Since(waitStart) > 30*time.Second {
				return nil, fmt.Errorf("container did not start within 30 seconds, inspect failed: %w", err)
			}
			time.Sleep(100 * time.Millisecond)
			continue
		}

		status := strings.TrimSpace(string(statusOutput))

		if status == "running" {
			break
		}

		if time.Since(waitStart) > 30*time.Second {
			return nil, fmt.Errorf("container did not start within 30 seconds, status: %q", status)
		}

		time.Sleep(100 * time.Millisecond)
	}

	// Configure SSH in the container (asynchronously)
	go func() {
		// Use a separate context with longer timeout for SSH setup
		sshCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()

		if err := m.setupContainerSSH(sshCtx, containerID, dockerHost, sshKeys); err != nil {
			log.Printf("Warning: Failed to setup SSH in container: %v", err)
		}
	}()

	// Get the host port that Docker mapped for SSH (container port 22)
	hostSSHPort, err := m.getContainerSSHPort(ctx, containerID, dockerHost)
	if err != nil {
		return nil, fmt.Errorf("failed to get container SSH port mapping: %w", err)
	}

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
		// SSH key material
		SSHServerIdentityKey: sshKeys.ServerIdentityKey,
		SSHAuthorizedKeys:    sshKeys.AuthorizedKeys,
		SSHCAPublicKey:       sshKeys.CAPublicKey,
		SSHHostCertificate:   sshKeys.HostCertificate,
		SSHClientPrivateKey:  sshKeys.ClientPrivateKey,
		SSHPort:              hostSSHPort, // Host port that Docker mapped for SSH access
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

	// Restart SSH daemon after container start (it doesn't persist across stops)
	log.Printf("[SSH] Restarting SSH daemon in container %s after start", containerID)
	sshCmd := exec.CommandContext(ctx, "docker", "exec", "-u", "root", "-d", container.PodName, "/usr/sbin/sshd", "-D", "-f", "/etc/ssh/sshd_config")
	if container.DockerHost != "" {
		sshCmd.Env = append(os.Environ(), fmt.Sprintf("DOCKER_HOST=%s", container.DockerHost))
	}
	if sshOutput, sshErr := sshCmd.CombinedOutput(); sshErr != nil {
		log.Printf("[SSH WARNING] Failed to restart SSH daemon in container %s: %v: %s", containerID, sshErr, sshOutput)
	} else {
		log.Printf("[SSH] SSH daemon restarted in container %s", containerID)
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

// SetupContainerSSH configures SSH inside the container (public method for migration/setup)
// This is used to add SSH configuration to existing containers that were created
// before SSH support was added to the system
func (m *DockerManager) SetupContainerSSH(ctx context.Context, containerID, dockerHost string, sshKeys *ContainerSSHKeys) error {
	return m.setupContainerSSH(ctx, containerID, dockerHost, sshKeys)
}

// setupContainerSSH configures SSH inside the container (internal method)
func (m *DockerManager) setupContainerSSH(ctx context.Context, containerID, dockerHost string, sshKeys *ContainerSSHKeys) error {
	// Wait for container to be ready by spinning until we can execute a simple command
	// This ensures the container is fully running before we try to set up SSH
	waitStart := time.Now()
	for {
		// Try a simple echo command to check if container is ready
		testCmd := exec.CommandContext(ctx, "docker", "exec", "-u", "root", containerID, "echo", "ready")
		if dockerHost != "" {
			testCmd.Env = append(os.Environ(), fmt.Sprintf("DOCKER_HOST=%s", dockerHost))
		}
		if _, err := testCmd.CombinedOutput(); err == nil {
			// Container is ready
			break
		}

		// Check if we've been waiting too long
		if time.Since(waitStart) > 30*time.Second {
			return fmt.Errorf("container not ready after 30 seconds")
		}

		// Check if context is cancelled
		select {
		case <-ctx.Done():
			return fmt.Errorf("context cancelled while waiting for container: %w", ctx.Err())
		default:
			// Short spin wait - 100ms
			time.Sleep(100 * time.Millisecond)
		}
	}

	// Copy /exe.dev SSH binaries to the container
	if err := m.copySSHBinaries(ctx, containerID, dockerHost); err != nil {
		return fmt.Errorf("failed to copy SSH binaries: %w", err)
	}

	// Create SSH config files inside the container
	cmds := [][]string{
		// Create SSH directories
		{"mkdir", "-p", "/etc/ssh", "/root/.ssh", "/run/sshd", "/var/empty", "/usr/lib/ssh"},
		{"chmod", "700", "/root/.ssh"},
		{"chmod", "755", "/var/empty"},
		// Create sshd user for privilege separation (required by OpenSSH)
		{"sh", "-c", "grep -q '^sshd:' /etc/passwd || echo 'sshd:x:22:22:sshd:/var/empty:/bin/false' >> /etc/passwd"},
		{"sh", "-c", "grep -q '^sshd:' /etc/group || echo 'sshd:x:22:' >> /etc/group"},
		// Create symlinks for dynamic loaders in standard locations
		// This ensures the binaries with hardcoded interpreter paths can find their loader
		{"sh", "-c", "if [ -f /exe.dev/lib/ld-linux-x86-64.so.2 ] && [ ! -e /lib64/ld-linux-x86-64.so.2 ]; then mkdir -p /lib64 && ln -sf /exe.dev/lib/ld-linux-x86-64.so.2 /lib64/; fi"},
		{"sh", "-c", "if [ -f /exe.dev/lib/ld-musl-aarch64.so.1 ] && [ ! -e /lib/ld-musl-aarch64.so.1 ]; then ln -sf /exe.dev/lib/ld-musl-aarch64.so.1 /lib/; fi"},
		{"sh", "-c", "if [ -f /exe.dev/lib/ld-musl-x86_64.so.1 ] && [ ! -e /lib/ld-musl-x86_64.so.1 ]; then ln -sf /exe.dev/lib/ld-musl-x86_64.so.1 /lib/; fi"},
	}

	// Execute setup commands
	for _, cmd := range cmds {
		execCmd := exec.CommandContext(ctx, "docker", append([]string{"exec", "-u", "root", containerID}, cmd...)...)
		if dockerHost != "" {
			execCmd.Env = append(os.Environ(), fmt.Sprintf("DOCKER_HOST=%s", dockerHost))
		}
		if output, err := execCmd.CombinedOutput(); err != nil {
			return fmt.Errorf("SSH setup command failed %v: %w: %s", cmd, err, output)
		}
	}

	// Create SSH daemon configuration for public key authentication
	sshConfig := `# SSH daemon configuration for public key authentication
Port 22
ListenAddress 0.0.0.0
PermitRootLogin yes
PasswordAuthentication no
PubkeyAuthentication yes
AuthorizedKeysFile /root/.ssh/authorized_keys
HostKey /etc/ssh/ssh_host_ed25519_key
LogLevel INFO
`

	// Extract server public key from the server identity key for the .pub file
	serverPrivKey, err := ssh.ParsePrivateKey([]byte(sshKeys.ServerIdentityKey))
	if err != nil {
		return fmt.Errorf("failed to parse server private key: %w", err)
	}
	serverPubKey := string(ssh.MarshalAuthorizedKey(serverPrivKey.PublicKey()))

	// Write SSH key files and configuration for public key auth
	files := map[string]string{
		"/etc/ssh/ssh_host_ed25519_key":     sshKeys.ServerIdentityKey,
		"/etc/ssh/ssh_host_ed25519_key.pub": serverPubKey,
		"/root/.ssh/authorized_keys":        sshKeys.AuthorizedKeys,
		"/etc/ssh/sshd_config":              sshConfig,
	}

	// Use install command to create files with correct permissions in one step
	for filePath, content := range files {
		// Set appropriate permissions: private keys 600, public keys and config 644
		mode := "600"
		if strings.HasSuffix(filePath, ".pub") || strings.HasSuffix(filePath, "sshd_config") {
			mode = "644"
		}
		cmd := exec.CommandContext(ctx, "docker", "exec", "-u", "root", "-i", containerID, "install", "-m", mode, "/dev/stdin", filePath)
		if dockerHost != "" {
			cmd.Env = append(os.Environ(), fmt.Sprintf("DOCKER_HOST=%s", dockerHost))
		}
		cmd.Stdin = strings.NewReader(content)
		if output, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("failed to install SSH file %s: %w: %s", filePath, err, output)
		}
	}

	// We now have our own SSH binaries in /exe.dev, so use them directly
	// First check if our sshd binary exists
	checkSSHDCmd := exec.CommandContext(ctx, "docker", "exec", "-u", "root", containerID, "test", "-x", "/exe.dev/bin/sshd")
	if dockerHost != "" {
		checkSSHDCmd.Env = append(os.Environ(), fmt.Sprintf("DOCKER_HOST=%s", dockerHost))
	}

	var sshdPath string
	if err := checkSSHDCmd.Run(); err == nil {
		// Use our embedded sshd directly
		sshdPath = "/exe.dev/bin/sshd"
		log.Printf("[SSH] Using embedded SSH daemon from /exe.dev")
	} else {
		// Fallback to system sshd if available
		fallbackCmd := exec.CommandContext(ctx, "docker", "exec", "-u", "root", containerID, "sh", "-c", "which sshd || which /usr/sbin/sshd || echo 'NO_SSHD'")
		if dockerHost != "" {
			fallbackCmd.Env = append(os.Environ(), fmt.Sprintf("DOCKER_HOST=%s", dockerHost))
		}
		output, _ := fallbackCmd.Output()
		sshdPath = strings.TrimSpace(string(output))

		if sshdPath == "NO_SSHD" || sshdPath == "" {
			log.Printf("[SSH] No SSH daemon available in container %s", containerID)
			return fmt.Errorf("no SSH daemon available")
		}
		log.Printf("[SSH] Using system SSH daemon: %s", sshdPath)
	}

	// Update sshd_config to use our embedded sftp-server if available
	if sshdPath == "/exe.dev/bin/sshd" {
		updateConfigCmd := exec.CommandContext(ctx, "docker", "exec", "-u", "root", containerID, "sh", "-c",
			`sed -i 's|^Subsystem sftp .*|Subsystem sftp /exe.dev/bin/sftp-server|' /etc/ssh/sshd_config`)
		if dockerHost != "" {
			updateConfigCmd.Env = append(os.Environ(), fmt.Sprintf("DOCKER_HOST=%s", dockerHost))
		}
		if output, err := updateConfigCmd.CombinedOutput(); err != nil {
			log.Printf("Warning: Failed to update sftp subsystem path: %v: %s", err, output)
		}
	}

	// Start SSH daemon in background using the found path
	startCmd := exec.CommandContext(ctx, "docker", "exec", "-u", "root", "-d", containerID, sshdPath, "-D", "-f", "/etc/ssh/sshd_config")
	if dockerHost != "" {
		startCmd.Env = append(os.Environ(), fmt.Sprintf("DOCKER_HOST=%s", dockerHost))
	}
	if output, err := startCmd.CombinedOutput(); err != nil {
		// Check if it's because sshd is already running
		if strings.Contains(string(output), "Address already in use") {
			log.Printf("[SSH] SSH daemon already running in container %s", containerID)
		} else {
			log.Printf("[SSH ERROR] Failed to start SSH daemon in container %s: %v: %s", containerID, err, output)
			// Try alternative startup method
			retryCmd := exec.CommandContext(ctx, "docker", "exec", "-u", "root", "-d", containerID, "sh", "-c", fmt.Sprintf("nohup %s -D -f /etc/ssh/sshd_config > /dev/null 2>&1 &", sshdPath))
			if dockerHost != "" {
				retryCmd.Env = append(os.Environ(), fmt.Sprintf("DOCKER_HOST=%s", dockerHost))
			}
			if retryOutput, retryErr := retryCmd.CombinedOutput(); retryErr != nil {
				log.Printf("[SSH ERROR] Retry also failed for container %s: %v: %s", containerID, retryErr, retryOutput)
			} else {
				log.Printf("[SSH] SSH daemon started via retry method in container %s", containerID)
			}
		}
	} else {
		log.Printf("[SSH] SSH daemon started successfully in container %s", containerID)
	}

	// Spin wait for SSH daemon to be running (up to 5 seconds)
	startTime := time.Now()
	for time.Since(startTime) < 5*time.Second {
		checkCmd := exec.CommandContext(ctx, "docker", "exec", "-u", "root", containerID, "sh", "-c", "ps aux | grep -v grep | grep -E 'sshd|ssh-'")
		if dockerHost != "" {
			checkCmd.Env = append(os.Environ(), fmt.Sprintf("DOCKER_HOST=%s", dockerHost))
		}
		if output, err := checkCmd.CombinedOutput(); err == nil {
			outputStr := strings.TrimSpace(string(output))
			if outputStr != "" {
				log.Printf("[SSH] SSH daemon verified running in container %s", containerID)
				return nil
			}
		} else if ctx.Err() != nil {
			// Context cancelled
			return ctx.Err()
		}
		// Short spin wait - 100ms
		time.Sleep(100 * time.Millisecond)
	}

	// Final check after timeout
	log.Printf("[SSH WARNING] SSH daemon may not be running in container %s after 5s", containerID)

	return nil
}

// getDockerHostArch gets the architecture of the Docker host
func (m *DockerManager) getDockerHostArch(ctx context.Context, dockerHost string) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", "version", "--format", "{{.Server.Arch}}")
	if dockerHost != "" {
		cmd.Env = append(os.Environ(), fmt.Sprintf("DOCKER_HOST=%s", dockerHost))
	}

	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to get Docker host architecture: %w", err)
	}

	arch := strings.TrimSpace(string(output))
	// Normalize architecture names
	switch arch {
	case "amd64", "x86_64":
		return "amd64", nil
	case "arm64", "aarch64":
		return "arm64", nil
	default:
		return "", fmt.Errorf("unsupported Docker host architecture: %s", arch)
	}
}

// copySSHBinaries copies the embedded SSH binaries to /exe.dev in the container
func (m *DockerManager) copySSHBinaries(ctx context.Context, containerID, dockerHost string) error {
	// Get the Docker host architecture
	arch, err := m.getDockerHostArch(ctx, dockerHost)
	if err != nil {
		return fmt.Errorf("failed to get Docker host architecture: %w", err)
	}

	// Get the embedded filesystem for this architecture
	rovolFS, err := GetRovolFS(arch)
	if err != nil {
		return fmt.Errorf("failed to get rovol filesystem: %w", err)
	}

	// Create a temporary directory to stage the files
	tmpDir, err := os.MkdirTemp("", "exe-rovol-*")
	if err != nil {
		return fmt.Errorf("failed to create temp directory: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	// Walk the embedded filesystem and copy files to temp directory
	err = fs.WalkDir(rovolFS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		destPath := filepath.Join(tmpDir, path)

		if d.IsDir() {
			return os.MkdirAll(destPath, 0755)
		}

		// Read the file from embedded FS
		data, err := fs.ReadFile(rovolFS, path)
		if err != nil {
			return fmt.Errorf("failed to read embedded file %s: %w", path, err)
		}

		// Write to temp directory
		return os.WriteFile(destPath, data, 0755)
	})
	if err != nil {
		return fmt.Errorf("failed to extract rovol files: %w", err)
	}

	// Create /exe.dev directory in container
	mkdirCmd := exec.CommandContext(ctx, "docker", "exec", "-u", "root", containerID, "mkdir", "-p", "/exe.dev")
	if dockerHost != "" {
		mkdirCmd.Env = append(os.Environ(), fmt.Sprintf("DOCKER_HOST=%s", dockerHost))
	}
	if output, err := mkdirCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to create /exe.dev directory: %w: %s", err, output)
	}

	// Copy the files to the container
	copyCmd := exec.CommandContext(ctx, "docker", "cp", tmpDir+"/.", containerID+":/exe.dev/")
	if dockerHost != "" {
		copyCmd.Env = append(os.Environ(), fmt.Sprintf("DOCKER_HOST=%s", dockerHost))
	}
	if output, err := copyCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to copy files to container: %w: %s", err, output)
	}

	// Make the sshd binary executable (should already be from docker cp, but ensure it)
	chmodCmd := exec.CommandContext(ctx, "docker", "exec", "-u", "root", containerID, "chmod", "+x", "/exe.dev/bin/sshd", "/exe.dev/bin/sftp-server", "/exe.dev/bin/sshd-session")
	if dockerHost != "" {
		chmodCmd.Env = append(os.Environ(), fmt.Sprintf("DOCKER_HOST=%s", dockerHost))
	}
	if output, err := chmodCmd.CombinedOutput(); err != nil {
		// Non-fatal, just log
		log.Printf("Warning: Failed to chmod SSH binaries: %v: %s", err, output)
	}

	log.Printf("[SSH] Copied SSH binaries for %s architecture to container %s", arch, containerID)
	return nil
}

// getContainerSSHPort gets the host port mapped to container port 22
func (m *DockerManager) getContainerSSHPort(ctx context.Context, containerID, dockerHost string) (int, error) {
	cmd := exec.CommandContext(ctx, "docker", "port", containerID, "22")
	if dockerHost != "" {
		cmd.Env = append(os.Environ(), fmt.Sprintf("DOCKER_HOST=%s", dockerHost))
	}

	output, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("failed to get container port: %w", err)
	}

	// Parse port output: Docker may return multiple lines for IPv4 and IPv6
	// e.g., "0.0.0.0:32768\n[::]:32768"
	// Take the first non-empty line
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	var portLine string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			portLine = line
			break
		}
	}

	if portLine == "" {
		return 0, fmt.Errorf("no port mapping found in output")
	}

	port, err := parseDockerPortMapping(portLine)
	if err != nil {
		return 0, fmt.Errorf("failed to parse port from line '%s': %w", portLine, err)
	}

	return port, nil
}

// parseDockerPortMapping parses Docker port output format
// Supports both IPv4 (0.0.0.0:32768) and IPv6 ([::]:32768) formats
func parseDockerPortMapping(portStr string) (int, error) {
	var portPart string

	if strings.HasPrefix(portStr, "[") {
		// IPv6 format: [::]:32768 or [2001:db8::1]:8080
		parts := strings.Split(portStr, "]:")
		if len(parts) != 2 {
			return 0, fmt.Errorf("invalid IPv6 port format: %s", portStr)
		}
		portPart = parts[1]
	} else {
		// IPv4 format: 0.0.0.0:32768
		parts := strings.Split(portStr, ":")
		if len(parts) != 2 {
			return 0, fmt.Errorf("invalid IPv4 port format: %s", portStr)
		}
		portPart = parts[1]
	}

	var port int
	if _, err := fmt.Sscanf(portPart, "%d", &port); err != nil {
		return 0, fmt.Errorf("failed to parse port: %w", err)
	}

	return port, nil
}
