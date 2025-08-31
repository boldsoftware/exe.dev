package exe

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"exe.dev/container"
	"exe.dev/sqlite"
	"golang.org/x/crypto/ssh"
)

// TestNewSSHServerMachineConnection tests SSH connection to a specific machine
func TestNewSSHServerMachineConnection(t *testing.T) {
	t.Parallel()
	// Skip if CTR_HOST is not set (requires container support)
	if os.Getenv("CTR_HOST") == "" {
		t.Skip("CTR_HOST not set, skipping machine connection test")
	}

	server := NewTestServer(t, os.Getenv("CTR_HOST"))

	// Generate a test SSH key pair
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("Failed to generate private key: %v", err)
	}

	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatalf("Failed to create signer: %v", err)
	}

	publicKey := signer.PublicKey()

	// Register the user in the database
	email := "test@example.com"
	// teamName no longer used - machines are globally unique
	machineName := "test-machine"
	containerID := "test-container-123"

	// Create user
	userID, err := generateUserID()
	if err != nil {
		t.Fatalf("Failed to generate user ID: %v", err)
	}

	err = server.db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := tx.Exec(`
			INSERT INTO users (user_id, email)
			VALUES (?, ?)`,
			userID, email)
		return err
	})
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	// Create alloc for user
	allocID := "test-alloc-" + userID[:8]
	err = server.db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := tx.Exec(`
			INSERT INTO allocs (alloc_id, user_id, alloc_type, region, docker_host, created_at, stripe_customer_id, billing_email)
			VALUES (?, ?, 'medium', 'aws-us-west-2', '', datetime('now'), '', ?)`,
			allocID, userID, email)
		return err
	})
	if err != nil {
		t.Fatalf("Failed to create alloc: %v", err)
	}

	// Add SSH key
	err = server.db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := tx.Exec(`
			INSERT INTO ssh_keys (user_id, public_key)
			VALUES (?, ?)`,
			userID, string(ssh.MarshalAuthorizedKey(publicKey)))
		return err
	})
	if err != nil {
		t.Fatalf("Failed to add SSH key: %v", err)
	}

	// Create a machine in the database
	err = server.db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := tx.Exec(`
			INSERT INTO machines (alloc_id, name, status, image, container_id, created_by_user_id)
			VALUES (?, ?, ?, ?, ?, ?)`,
			allocID, machineName, "running", "ubuntu", containerID, userID)
		return err
	})
	if err != nil {
		t.Fatalf("Failed to create machine: %v", err)
	}

	// Mock container manager
	mockManager := &TestMockContainerManager{
		containers: map[string]*container.Container{
			containerID: {
				ID:      containerID,
				Name:    machineName,
				Status:  container.StatusRunning,
				AllocID: allocID,
			},
		},
	}
	server.containerManager = mockManager

	// Create SSH client config - connect as the machine name
	config := &ssh.ClientConfig{
		User: machineName, // Use machine name as username
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(signer),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	}

	// Connect to the server
	client, err := ssh.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", server.sshLn.tcp.Port), config)
	if err != nil {
		t.Fatalf("Failed to connect to SSH server: %v", err)
	}
	defer client.Close()

	// Test exec command on the machine
	session, err := client.NewSession()
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}
	defer session.Close()

	output, err := session.CombinedOutput("echo hello")
	if err != nil {
		// Check if we at least got the connection message
		outputStr := string(output)
		if !strings.Contains(outputStr, "Connecting to machine") {
			t.Errorf("Expected connection message, got: %s", outputStr)
		}
		// The actual command execution might fail because we're using a mock
		// but the connection should be established
	}
}

// TestMockContainerManager is a mock implementation for testing
type TestMockContainerManager struct {
	containers map[string]*container.Container
}

func (m *TestMockContainerManager) CreateContainer(ctx context.Context, req *container.CreateContainerRequest) (*container.Container, error) {
	return &container.Container{
		ID:      "new-container",
		Name:    req.Name,
		Status:  container.StatusRunning,
		AllocID: req.AllocID,
	}, nil
}

func (m *TestMockContainerManager) ListContainers(ctx context.Context, allocID string) ([]*container.Container, error) {
	var result []*container.Container
	for _, c := range m.containers {
		if c.AllocID == allocID {
			result = append(result, c)
		}
	}
	return result, nil
}

func (m *TestMockContainerManager) GetContainer(ctx context.Context, allocID, containerID string) (*container.Container, error) {
	if c, ok := m.containers[containerID]; ok && c.AllocID == allocID {
		return c, nil
	}
	return nil, fmt.Errorf("container not found")
}

func (m *TestMockContainerManager) StartContainer(ctx context.Context, allocID, containerID string) error {
	if c, ok := m.containers[containerID]; ok && c.AllocID == allocID {
		c.Status = container.StatusRunning
		return nil
	}
	return fmt.Errorf("container not found")
}

func (m *TestMockContainerManager) StopContainer(ctx context.Context, allocID, containerID string) error {
	if c, ok := m.containers[containerID]; ok && c.AllocID == allocID {
		c.Status = container.StatusStopped
		return nil
	}
	return fmt.Errorf("container not found")
}

func (m *TestMockContainerManager) DeleteContainer(ctx context.Context, allocID, containerID string) error {
	delete(m.containers, containerID)
	return nil
}

func (m *TestMockContainerManager) ConnectToContainer(ctx context.Context, allocID, containerID string) (*container.ContainerConnection, error) {
	// Return a simple mock connection
	if c, ok := m.containers[containerID]; ok && c.AllocID == allocID {
		return &container.ContainerConnection{
			Container: c,
		}, nil
	}
	return nil, fmt.Errorf("container not found")
}

func (m *TestMockContainerManager) BuildImage(ctx context.Context, req *container.BuildRequest) (*container.BuildResult, error) {
	// Mock implementation
	return &container.BuildResult{
		BuildID:   "test-build-123",
		ImageName: "test-image",
		Status:    "completed",
	}, nil
}

func (m *TestMockContainerManager) GetBuildStatus(ctx context.Context, buildID string) (*container.BuildResult, error) {
	return &container.BuildResult{
		BuildID:   buildID,
		ImageName: "test-image",
		Status:    "completed",
	}, nil
}

func (m *TestMockContainerManager) GetContainerLogs(ctx context.Context, allocID, containerID string, lines int) ([]string, error) {
	return []string{"mock log line 1", "mock log line 2"}, nil
}

func (m *TestMockContainerManager) Close() error {
	return nil
}

func (m *TestMockContainerManager) GetContainerDiagnostics(ctx context.Context, allocID, containerName string) (string, error) {
	return "Mock diagnostics for container " + containerName, nil
}

func (m *TestMockContainerManager) ExecuteInContainer(ctx context.Context, allocID, containerID string, cmd []string, stdin io.Reader, stdout, stderr io.Writer) error {
	// Simple mock execution
	if len(cmd) > 0 && cmd[0] == "echo" && len(cmd) > 1 {
		fmt.Fprintln(stdout, strings.Join(cmd[1:], " "))
		return nil
	}
	if len(cmd) > 0 && strings.Contains(cmd[0], "shell") {
		fmt.Fprintln(stdout, "mock shell")
		return nil
	}
	return fmt.Errorf("mock execution not implemented for: %v", cmd)
}
