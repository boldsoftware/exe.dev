package exe

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"exe.dev/container"
)

// MockContainerManager implements the container.Manager interface for testing
type MockContainerManager struct {
	containers map[string]*container.Container
	execCalls  []MockExecCall
}

// MockExecCall records calls to ExecuteInContainer for testing
type MockExecCall struct {
	UserID      string
	ContainerID string
	Command     []string
	Input       string
	Output      string
	Error       error
}

// NewMockContainerManager creates a new mock container manager
func NewMockContainerManager() *MockContainerManager {
	return &MockContainerManager{
		containers: make(map[string]*container.Container),
		execCalls:  make([]MockExecCall, 0),
	}
}

// AddContainer adds a pre-configured container for testing
func (m *MockContainerManager) AddContainer(containerID, name, userID, allocID string) {
	m.containers[containerID] = &container.Container{
		ID:        containerID,
		UserID:    userID,
		Name:      name,
		AllocID:   allocID,
		Status:    container.StatusRunning,
		CreatedAt: time.Now(),
		StartedAt: func() *time.Time { t := time.Now(); return &t }(),
	}
}

// CreateContainer creates a mock container
func (m *MockContainerManager) CreateContainer(ctx context.Context, req *container.CreateContainerRequest) (*container.Container, error) {
	containerID := fmt.Sprintf("mock-%s-%s", req.UserID, req.Name)

	c := &container.Container{
		ID:        containerID,
		UserID:    req.UserID,
		Name:      req.Name,
		AllocID:   req.AllocID,
		Image:     req.Image,
		Status:    container.StatusRunning,
		CreatedAt: time.Now(),
		StartedAt: func() *time.Time { t := time.Now(); return &t }(),
	}

	m.containers[containerID] = c
	return c, nil
}

// GetContainer retrieves a mock container
func (m *MockContainerManager) GetContainer(ctx context.Context, userID, containerID string) (*container.Container, error) {
	c, exists := m.containers[containerID]
	if !exists {
		return nil, fmt.Errorf("container not found")
	}
	return c, nil
}

// ListContainers lists mock containers
func (m *MockContainerManager) ListContainers(ctx context.Context, userID string) ([]*container.Container, error) {
	var result []*container.Container
	for _, c := range m.containers {
		if c.UserID == userID {
			result = append(result, c)
		}
	}
	return result, nil
}

// StartContainer starts a mock container
func (m *MockContainerManager) StartContainer(ctx context.Context, userID, containerID string) error {
	c, exists := m.containers[containerID]
	if !exists {
		return fmt.Errorf("container not found")
	}
	c.Status = container.StatusRunning
	return nil
}

// StopContainer stops a mock container
func (m *MockContainerManager) StopContainer(ctx context.Context, userID, containerID string) error {
	c, exists := m.containers[containerID]
	if !exists {
		return fmt.Errorf("container not found")
	}
	if c.Status != container.StatusRunning {
		return fmt.Errorf("container is not running (status: %s)", c.Status)
	}
	c.Status = container.StatusStopped
	return nil
}

// DeleteContainer deletes a mock container
func (m *MockContainerManager) DeleteContainer(ctx context.Context, userID, containerID string) error {
	delete(m.containers, containerID)
	return nil
}

// BuildImage builds a mock image
func (m *MockContainerManager) BuildImage(ctx context.Context, req *container.BuildRequest) (*container.BuildResult, error) {
	return &container.BuildResult{
		BuildID:   req.BuildID,
		ImageName: fmt.Sprintf("mock-image-%s", req.BuildID),
		Status:    "completed",
	}, nil
}

// GetBuildStatus gets mock build status
func (m *MockContainerManager) GetBuildStatus(ctx context.Context, buildID string) (*container.BuildResult, error) {
	return &container.BuildResult{
		BuildID:   buildID,
		ImageName: fmt.Sprintf("mock-image-%s", buildID),
		Status:    "completed",
	}, nil
}

// GetContainerLogs gets mock container logs
func (m *MockContainerManager) GetContainerLogs(ctx context.Context, userID, containerID string, lines int) ([]string, error) {
	return []string{"mock log line 1", "mock log line 2"}, nil
}

// ConnectToContainer creates a mock connection
func (m *MockContainerManager) ConnectToContainer(ctx context.Context, userID, containerID string) (*container.ContainerConnection, error) {
	c, exists := m.containers[containerID]
	if !exists {
		return nil, fmt.Errorf("container not found")
	}

	if c.Status != container.StatusRunning {
		return nil, fmt.Errorf("container is not running (status: %s)", c.Status)
	}

	return &container.ContainerConnection{
		Container: c,
		LocalPort: 0,
		StopFunc:  func() {},
	}, nil
}

// ExecuteInContainer simulates command execution
func (m *MockContainerManager) ExecuteInContainer(ctx context.Context, userID, containerID string, cmd []string, stdin io.Reader, stdout, stderr io.Writer) error {
	// Check if container exists and is running
	c, exists := m.containers[containerID]
	if !exists {
		return fmt.Errorf("container not found")
	}

	if c.Status != container.StatusRunning {
		return fmt.Errorf("container is not running (status: %s)", c.Status)
	}

	// Check if this is an interactive bash session
	if len(cmd) > 0 && cmd[0] == "/bin/bash" {
		// Generate hostname similar to real implementation
		var hostname string
		if c.AllocID != "" {
			hostname = fmt.Sprintf("%s.exe.dev", c.Name)
		} else {
			hostname = fmt.Sprintf("%s.exe.dev", c.Name)
		}

		// Show initial prompt
		prompt := fmt.Sprintf("root@%s:/workspace# ", hostname)
		if stdout != nil {
			stdout.Write([]byte(prompt))
		}

		// Handle interactive shell session only if stdin is provided and readable
		if stdin != nil {
			// Handle the interactive session
			done := make(chan bool, 1)
			go func() {
				for {
					// Read input character by character to handle interactive typing
					line := ""
					for {
						b := make([]byte, 1)
						n, err := stdin.Read(b)
						if err != nil {
							done <- true
							return
						}
						if n == 0 {
							continue
						}

						// Handle special characters
						switch b[0] {
						case '\n', '\r':
							// End of line - process the command
							if stdout != nil {
								stdout.Write([]byte("\n"))
							}
							goto processCommand
						case 3: // Ctrl+C
							done <- true
							return
						case 4: // Ctrl+D
							if len(line) == 0 {
								done <- true
								return
							}
						default:
							// Normal character - add to line
							line += string(b[0])
							// Echo the character (terminal echo)
							if stdout != nil {
								stdout.Write(b)
							}
						}
					}

				processCommand:
					// Handle special commands
					trimmedLine := strings.TrimSpace(line)
					if trimmedLine == "exit" {
						if stdout != nil {
							stdout.Write([]byte("logout\n"))
						}
						done <- true
						return
					}

					// Simulate command execution
					if trimmedLine != "" {
						// Simple command simulation
						switch {
						case strings.HasPrefix(trimmedLine, "ls"):
							if stdout != nil {
								stdout.Write([]byte("file1.txt  file2.txt  directory/\n"))
							}
						case strings.HasPrefix(trimmedLine, "pwd"):
							if stdout != nil {
								stdout.Write([]byte("/workspace\n"))
							}
						case strings.HasPrefix(trimmedLine, "whoami"):
							if stdout != nil {
								stdout.Write([]byte("root\n"))
							}
						case strings.HasPrefix(trimmedLine, "cd"):
							// Handle cd command (just acknowledge)
							// Don't output anything
						case strings.HasPrefix(trimmedLine, "echo "):
							// Echo command - return the text after "echo "
							text := strings.TrimPrefix(trimmedLine, "echo ")
							if stdout != nil {
								stdout.Write([]byte(text + "\n"))
							}
						default:
							// For unknown commands, show a simple error
							if stdout != nil && trimmedLine != "" {
								// Don't show error for empty lines
							}
						}
					}

					// Show prompt again for next command
					if stdout != nil {
						stdout.Write([]byte(prompt))
					}
				}
			}()

			// Wait for either completion or timeout (for tests)
			select {
			case <-done:
				// Normal completion
			case <-time.After(100 * time.Millisecond):
				// Timeout - this is likely a test that's not providing interactive input
				// Just return without hanging
			case <-ctx.Done():
				// Context cancelled
			}
		}

		// Record the interactive session
		call := MockExecCall{
			UserID:      userID,
			ContainerID: containerID,
			Command:     cmd,
			Input:       "interactive session",
			Output:      "interactive bash session",
			Error:       nil,
		}
		m.execCalls = append(m.execCalls, call)

	} else {
		// Non-interactive command execution
		var output string
		if len(cmd) > 0 {
			switch cmd[0] {
			case "echo":
				// Echo command - return the arguments
				if len(cmd) > 1 {
					output = strings.Join(cmd[1:], " ") + "\n"
				} else {
					output = "\n"
				}
			case "pwd":
				output = "/workspace\n"
			case "whoami":
				output = "root\n"
			case "ls":
				output = "file1.txt  file2.txt  directory/\n"
			default:
				// Handle SCP commands specially
				if len(cmd) >= 2 && cmd[0] == "scp" {
					// SCP protocol - return minimal/no output for success
					// SCP -t (target mode) expects silence on success
					if cmd[1] == "-t" {
						output = "" // Silent success for SCP target mode
					} else {
						output = "" // Silent success for other SCP modes
					}
				} else if len(cmd) >= 3 && cmd[0] == "sh" && cmd[1] == "-c" {
					// Handle shell commands
					shellCmd := cmd[2]
					if strings.Contains(shellCmd, "getent passwd") && strings.Contains(shellCmd, "cut -d: -f7") {
						// Return a shell path for the getent passwd command
						output = "/bin/bash\n"
					} else {
						// For other shell commands, return generic executed message
						output = fmt.Sprintf("Executed: %v\n", cmd)
					}
				} else {
					// For other commands, return generic executed message
					output = fmt.Sprintf("Executed: %v\n", cmd)
				}
			}
		} else {
			output = "\n"
		}

		// Record the call
		call := MockExecCall{
			UserID:      userID,
			ContainerID: containerID,
			Command:     cmd,
			Input:       "",
			Output:      output,
			Error:       nil,
		}
		m.execCalls = append(m.execCalls, call)

		// Write output
		if stdout != nil {
			stdout.Write([]byte(output))
		}
	}

	return nil
}

// Close cleans up mock resources
func (m *MockContainerManager) Close() error {
	return nil
}

// GetExecCalls returns recorded exec calls for testing
func (m *MockContainerManager) GetExecCalls() []MockExecCall {
	return m.execCalls
}

// SetContainerStatus sets a container's status for testing
func (m *MockContainerManager) SetContainerStatus(containerID string, status container.ContainerStatus) {
	if c, exists := m.containers[containerID]; exists {
		c.Status = status
	}
}

// GetContainerDiagnostics returns mock diagnostics for testing
func (m *MockContainerManager) GetContainerDiagnostics(ctx context.Context, userID, containerName string) (string, error) {
	return fmt.Sprintf("Mock diagnostics for container '%s': Status OK", containerName), nil
}
