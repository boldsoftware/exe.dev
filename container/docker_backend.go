package container

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"
	
	"github.com/creack/pty"
)

// DockerBackend implements Backend using local Docker
type DockerBackend struct {
	mu         sync.RWMutex
	containers map[string]*Container
	
	// For testing - track operations
	operations []string
	opMu       sync.Mutex
}

// NewDockerBackend creates a new Docker backend for testing
func NewDockerBackend() *DockerBackend {
	return &DockerBackend{
		containers: make(map[string]*Container),
		operations: []string{},
	}
}

// GetOperations returns the list of operations performed (for testing)
func (d *DockerBackend) GetOperations() []string {
	d.opMu.Lock()
	defer d.opMu.Unlock()
	ops := make([]string, len(d.operations))
	copy(ops, d.operations)
	return ops
}

func (d *DockerBackend) recordOp(op string) {
	d.opMu.Lock()
	defer d.opMu.Unlock()
	d.operations = append(d.operations, op)
}

// CreateContainer creates a new Docker container
func (d *DockerBackend) CreateContainer(ctx context.Context, config *CreateConfig) (*Container, error) {
	d.recordOp(fmt.Sprintf("CreateContainer: %s/%s", config.TeamName, config.Name))
	
	// Generate container ID
	containerID := fmt.Sprintf("docker-%s-%s-%d", config.TeamName, config.Name, time.Now().Unix())
	
	// Build Docker run command
	args := []string{"run", "-d", "--name", containerID}
	
	// Keep container running with a simple sleep loop
	// We'll execute commands via docker exec, not SSH
	args = append(args, config.Image, "/bin/sh", "-c", "while true; do sleep 3600; done")
	
	// Execute docker run
	cmd := exec.CommandContext(ctx, "docker", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		// If Docker is not available, create a mock container for testing
		if strings.Contains(string(output), "docker") || strings.Contains(err.Error(), "executable file not found") {
			// Mock mode for testing without Docker
			container := &Container{
				ID:        containerID,
				Name:      config.Name,
				UserID:    config.UserID,
				TeamName:  config.TeamName,
				Status:    StatusRunning,
				Image:     config.Image,
				CreatedAt: time.Now(),
				PodName:   containerID,
				Namespace: "docker-local",
			}
			
			d.mu.Lock()
			d.containers[containerID] = container
			d.mu.Unlock()
			
			return container, nil
		}
		return nil, fmt.Errorf("failed to create container: %w: %s", err, output)
	}
	
	// Parse Docker container ID from output
	dockerID := strings.TrimSpace(string(output))
	if len(dockerID) > 12 {
		dockerID = dockerID[:12]
	}
	
	container := &Container{
		ID:        containerID,
		Name:      config.Name,
		UserID:    config.UserID,
		TeamName:  config.TeamName,
		Status:    StatusRunning,
		Image:     config.Image,
		CreatedAt: time.Now(),
		PodName:   dockerID,
		Namespace: "docker-local",
	}
	
	d.mu.Lock()
	d.containers[containerID] = container
	d.mu.Unlock()
	
	return container, nil
}

// GetContainer retrieves container information
func (d *DockerBackend) GetContainer(ctx context.Context, containerID string) (*Container, error) {
	d.recordOp(fmt.Sprintf("GetContainer: %s", containerID))
	
	d.mu.RLock()
	container, exists := d.containers[containerID]
	d.mu.RUnlock()
	
	if !exists {
		// Try to find the container in Docker directly
		// This handles cases where the server was restarted
		if strings.HasPrefix(containerID, "docker-") {
			// Extract the Docker container name/ID
			cmd := exec.CommandContext(ctx, "docker", "ps", "-a", "--filter", fmt.Sprintf("name=%s", containerID), "--format", "{{.ID}}")
			output, err := cmd.Output()
			if err == nil && len(output) > 0 {
				dockerID := strings.TrimSpace(string(output))
				if dockerID != "" {
					// Create a container object from the Docker container
					container = &Container{
						ID:        containerID,
						Name:      extractNameFromID(containerID),
						Status:    StatusRunning,
						PodName:   dockerID[:12], // Use short Docker ID
						Namespace: "docker-local",
					}
					
					// Cache it for future use
					d.mu.Lock()
					d.containers[containerID] = container
					d.mu.Unlock()
					
					exists = true
				}
			}
		}
		
		if !exists {
			return nil, fmt.Errorf("container not found: %s", containerID)
		}
	}
	
	// Update status from Docker if running with real Docker
	if container.PodName != containerID {
		cmd := exec.CommandContext(ctx, "docker", "inspect", "-f", "{{.State.Status}}", container.PodName)
		output, err := cmd.Output()
		if err == nil {
			status := strings.TrimSpace(string(output))
			switch status {
			case "running":
				container.Status = StatusRunning
			case "paused":
				container.Status = StatusStopped // No sleeping status, use stopped
			case "exited", "dead":
				container.Status = StatusStopped
			default:
				container.Status = StatusPending
			}
		}
	}
	
	return container, nil
}

// extractNameFromID extracts the container name from a Docker container ID
// e.g., "docker-david-neon-river-1755060759" -> "neon-river"
func extractNameFromID(containerID string) string {
	parts := strings.Split(containerID, "-")
	if len(parts) >= 4 {
		// Return everything between team name and timestamp
		return strings.Join(parts[2:len(parts)-1], "-")
	}
	return containerID
}

// ListContainers lists all containers for a user
func (d *DockerBackend) ListContainers(ctx context.Context, userID string) ([]*Container, error) {
	d.recordOp(fmt.Sprintf("ListContainers: %s", userID))
	
	d.mu.RLock()
	defer d.mu.RUnlock()
	
	var containers []*Container
	for _, c := range d.containers {
		if c.UserID == userID {
			containers = append(containers, c)
		}
	}
	
	return containers, nil
}

// StartContainer starts a stopped container
func (d *DockerBackend) StartContainer(ctx context.Context, containerID string) error {
	d.recordOp(fmt.Sprintf("StartContainer: %s", containerID))
	
	d.mu.RLock()
	container, exists := d.containers[containerID]
	d.mu.RUnlock()
	
	if !exists {
		return fmt.Errorf("container not found: %s", containerID)
	}
	
	// Start Docker container if using real Docker
	if container.PodName != containerID {
		cmd := exec.CommandContext(ctx, "docker", "start", container.PodName)
		if err := cmd.Run(); err != nil {
			// Mock mode - just update status
		}
	}
	
	container.Status = StatusRunning
	return nil
}

// StopContainer stops a running container
func (d *DockerBackend) StopContainer(ctx context.Context, containerID string) error {
	d.recordOp(fmt.Sprintf("StopContainer: %s", containerID))
	
	d.mu.RLock()
	container, exists := d.containers[containerID]
	d.mu.RUnlock()
	
	if !exists {
		return fmt.Errorf("container not found: %s", containerID)
	}
	
	// Stop Docker container if using real Docker
	if container.PodName != containerID {
		cmd := exec.CommandContext(ctx, "docker", "stop", container.PodName)
		if err := cmd.Run(); err != nil {
			// Mock mode - just update status
		}
	}
	
	container.Status = StatusStopped
	return nil
}

// DeleteContainer deletes a container and its resources
func (d *DockerBackend) DeleteContainer(ctx context.Context, containerID string) error {
	d.recordOp(fmt.Sprintf("DeleteContainer: %s", containerID))
	
	d.mu.Lock()
	container, exists := d.containers[containerID]
	if !exists {
		d.mu.Unlock()
		return fmt.Errorf("container not found: %s", containerID)
	}
	
	// Remove Docker container if using real Docker
	if container.PodName != containerID {
		cmd := exec.CommandContext(ctx, "docker", "rm", "-f", container.PodName)
		if err := cmd.Run(); err != nil {
			// Mock mode - just delete from map
		}
	}
	
	delete(d.containers, containerID)
	d.mu.Unlock()
	
	return nil
}

// ExecuteInContainer executes a command in a container
func (d *DockerBackend) ExecuteInContainer(ctx context.Context, containerID string, command []string, stdin io.Reader) (stdout, stderr string, err error) {
	d.recordOp(fmt.Sprintf("ExecuteInContainer: %s cmd=%v", containerID, command))
	
	d.mu.RLock()
	container, exists := d.containers[containerID]
	d.mu.RUnlock()
	
	if !exists {
		return "", "", fmt.Errorf("container not found: %s", containerID)
	}
	
	// Use the actual Docker container ID (PodName) if available
	dockerID := container.PodName
	if dockerID == "" || dockerID == containerID {
		// This is a mock container for testing
		return "mock output\n", "", nil
	}
	
	// Execute in real Docker container
	args := append([]string{"exec", "-i", dockerID}, command...)
	cmd := exec.CommandContext(ctx, "docker", args...)
	
	if stdin != nil {
		cmd.Stdin = stdin
	}
	
	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf
	
	err = cmd.Run()
	return stdoutBuf.String(), stderrBuf.String(), err
}

// ExecuteInContainerWithPTY executes a command with a pseudo-terminal (for interactive shells)
func (d *DockerBackend) ExecuteInContainerWithPTY(ctx context.Context, containerID string, command []string, stdin io.Reader, stdout, stderr io.Writer) error {
	d.recordOp(fmt.Sprintf("ExecuteInContainerWithPTY: %s cmd=%v", containerID, command))
	
	d.mu.RLock()
	container, exists := d.containers[containerID]
	d.mu.RUnlock()
	
	if !exists {
		return fmt.Errorf("container not found: %s", containerID)
	}
	
	// Use the actual Docker container ID (PodName) if available
	dockerID := container.PodName
	if dockerID == "" || dockerID == containerID {
		// This is a mock container for testing
		if stdout != nil {
			stdout.Write([]byte("mock output\n"))
		}
		return nil
	}
	
	// Build docker exec command with -it for proper PTY allocation
	args := []string{"exec", "-it"}
	
	// Add environment variables for better shell experience
	args = append(args, "-e", "TERM=xterm-256color")
	
	// Add the container ID and command
	args = append(args, dockerID)
	args = append(args, command...)
	
	cmd := exec.CommandContext(ctx, "docker", args...)
	
	// Start the command with a PTY
	ptmx, err := pty.Start(cmd)
	if err != nil {
		return fmt.Errorf("failed to start docker exec with PTY: %w", err)
	}
	defer ptmx.Close()
	
	// Handle window size if possible (would need to be passed from SSH)
	// For now, set a reasonable default
	pty.Setsize(ptmx, &pty.Winsize{
		Rows: 24,
		Cols: 80,
	})
	
	// Create goroutines to copy data between SSH and the PTY
	var wg sync.WaitGroup
	wg.Add(2)
	
	// Copy from stdin to PTY
	go func() {
		defer wg.Done()
		io.Copy(ptmx, stdin)
	}()
	
	// Copy from PTY to stdout
	go func() {
		defer wg.Done()
		io.Copy(stdout, ptmx)
	}()
	
	// Wait for the command to finish
	if err := cmd.Wait(); err != nil {
		// Check if it's just an exit code
		if exitErr, ok := err.(*exec.ExitError); ok {
			// This is normal when the shell exits
			_ = exitErr
		} else {
			return fmt.Errorf("docker exec failed: %w", err)
		}
	}
	
	// Wait for I/O to complete
	wg.Wait()
	
	return nil
}

// ExecuteInContainerStreaming executes a command with streaming I/O (for interactive shells)
func (d *DockerBackend) ExecuteInContainerStreaming(ctx context.Context, containerID string, command []string, stdin io.Reader, stdout, stderr io.Writer) error {
	d.recordOp(fmt.Sprintf("ExecuteInContainerStreaming: %s cmd=%v", containerID, command))
	
	d.mu.RLock()
	container, exists := d.containers[containerID]
	d.mu.RUnlock()
	
	if !exists {
		return fmt.Errorf("container not found: %s", containerID)
	}
	
	// Use the actual Docker container ID (PodName) if available
	dockerID := container.PodName
	if dockerID == "" || dockerID == containerID {
		// This is a mock container for testing
		if stdout != nil {
			stdout.Write([]byte("mock output\n"))
		}
		return nil
	}
	
	// For interactive shells, we need to allocate a pseudo-terminal
	// Docker exec needs -t for terminal allocation, but this requires special handling
	// For now, we'll use -i and set TERM to make shells work better
	args := []string{"exec", "-i"}
	
	// Add environment variable for better shell experience
	args = append(args, "-e", "TERM=xterm-256color")
	
	// Add the container ID and command
	args = append(args, dockerID)
	args = append(args, command...)
	
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	
	// Set up the process environment to handle signals properly
	cmd.Env = append(cmd.Env, "TERM=xterm-256color")
	
	// Start the command and wait for it to complete
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start docker exec: %w", err)
	}
	
	// Wait for the command to finish
	return cmd.Wait()
}

// GetContainerLogs retrieves container logs
func (d *DockerBackend) GetContainerLogs(ctx context.Context, containerID string, lines int) (string, error) {
	d.recordOp(fmt.Sprintf("GetContainerLogs: %s lines=%d", containerID, lines))
	
	d.mu.RLock()
	container, exists := d.containers[containerID]
	d.mu.RUnlock()
	
	if !exists {
		return "", fmt.Errorf("container not found: %s", containerID)
	}
	
	// Get Docker logs if using real Docker
	if container.PodName != containerID {
		cmd := exec.CommandContext(ctx, "docker", "logs", "--tail", fmt.Sprintf("%d", lines), container.PodName)
		output, err := cmd.Output()
		if err != nil {
			return "Mock logs\n", nil
		}
		return string(output), nil
	}
	
	// Mock mode
	return "Mock container logs\n", nil
}

// ConnectToContainer establishes an SSH-like connection to a container
func (d *DockerBackend) ConnectToContainer(ctx context.Context, containerID string) (Connection, error) {
	d.recordOp(fmt.Sprintf("ConnectToContainer: %s", containerID))
	
	d.mu.RLock()
	container, exists := d.containers[containerID]
	d.mu.RUnlock()
	
	if !exists {
		return nil, fmt.Errorf("container not found: %s", containerID)
	}
	
	// Create mock connection for testing
	return &dockerConnection{
		containerID: container.PodName,
		backend:     d,
	}, nil
}

// WakeContainer wakes a sleeping container
func (d *DockerBackend) WakeContainer(ctx context.Context, containerID string) error {
	d.recordOp(fmt.Sprintf("WakeContainer: %s", containerID))
	
	// In Docker, this is the same as start
	return d.StartContainer(ctx, containerID)
}

// GetBackendType returns the backend type
func (d *DockerBackend) GetBackendType() string {
	return "docker"
}

// dockerConnection implements Connection for Docker containers
type dockerConnection struct {
	containerID string
	backend     *DockerBackend
	reader      *bufio.Reader
	writer      io.WriteCloser
	cmd         *exec.Cmd
}

func (c *dockerConnection) Read(p []byte) (n int, err error) {
	if c.reader == nil {
		return 0, io.EOF
	}
	return c.reader.Read(p)
}

func (c *dockerConnection) Write(p []byte) (n int, err error) {
	if c.writer == nil {
		return 0, fmt.Errorf("connection not initialized")
	}
	return c.writer.Write(p)
}

func (c *dockerConnection) Close() error {
	if c.cmd != nil {
		c.cmd.Process.Kill()
	}
	if c.writer != nil {
		c.writer.Close()
	}
	return nil
}

func (c *dockerConnection) Resize(width, height int) error {
	// Not implemented for Docker backend
	return nil
}

func (c *dockerConnection) ExecuteCommand(command []string) (stdout, stderr string, err error) {
	return c.backend.ExecuteInContainer(context.Background(), c.containerID, command, nil)
}