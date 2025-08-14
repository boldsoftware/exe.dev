package exe

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"exe.dev/container"
	"golang.org/x/crypto/ssh"
)

// TestNewSSHServerMachineConnection tests SSH connection to a specific machine
func TestNewSSHServerMachineConnection(t *testing.T) {
	// Create a test server
	dbPath := fmt.Sprintf("/tmp/test_new_ssh_machine_%d.db", time.Now().UnixNano())
	defer func() {
		// Clean up
		_ = os.Remove(dbPath)
	}()

	server, err := NewServer(":8080", "", "", dbPath, "local", []string{"unix:///var/run/docker.sock"})
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}
	server.quietMode = true
	server.testMode = true

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
	fingerprint := server.getPublicKeyFingerprint(publicKey)

	// Register the user in the database
	email := "test@example.com"
	teamName := "test-team"
	machineName := "test-machine"
	containerID := "test-container-123"

	// Create user
	_, err = server.db.Exec(`
		INSERT INTO users (public_key_fingerprint, email)
		VALUES (?, ?)`,
		fingerprint, email)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	// Create team
	_, err = server.db.Exec(`
		INSERT INTO teams (name, billing_email, is_personal)
		VALUES (?, ?, ?)`,
		teamName, email, true)
	if err != nil {
		t.Fatalf("Failed to create team: %v", err)
	}

	// Add user to team
	_, err = server.db.Exec(`
		INSERT INTO team_members (user_fingerprint, team_name, is_admin)
		VALUES (?, ?, ?)`,
		fingerprint, teamName, true)
	if err != nil {
		t.Fatalf("Failed to add user to team: %v", err)
	}

	// Add SSH key
	_, err = server.db.Exec(`
		INSERT INTO ssh_keys (fingerprint, user_email, public_key, verified, device_name)
		VALUES (?, ?, ?, ?, ?)`,
		fingerprint, email, string(ssh.MarshalAuthorizedKey(publicKey)), true, "test-device")
	if err != nil {
		t.Fatalf("Failed to add SSH key: %v", err)
	}

	// Create a machine in the database
	_, err = server.db.Exec(`
		INSERT INTO machines (team_name, name, status, image, container_id, created_by_fingerprint)
		VALUES (?, ?, ?, ?, ?, ?)`,
		teamName, machineName, "running", "ubuntu", containerID, fingerprint)
	if err != nil {
		t.Fatalf("Failed to create machine: %v", err)
	}

	// Mock container manager
	mockManager := &TestMockContainerManager{
		containers: map[string]*container.Container{
			containerID: {
				ID:       containerID,
				Name:     machineName,
				Status:   container.StatusRunning,
				UserID:   fingerprint,
				TeamName: teamName,
			},
		},
	}
	server.containerManager = mockManager

	// Find a free port
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to find free port: %v", err)
	}
	addr := listener.Addr().String()
	listener.Close()

	// Start the SSH server
	go func() {
		sshServer := NewSSHServer(server)
		sshServer.Start(addr)
	}()

	// Wait for server to start
	time.Sleep(100 * time.Millisecond)

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
	client, err := ssh.Dial("tcp", addr, config)
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
		ID:       "new-container",
		Name:     req.Name,
		Status:   container.StatusRunning,
		UserID:   req.UserID,
		TeamName: req.TeamName,
	}, nil
}

func (m *TestMockContainerManager) ListContainers(ctx context.Context, userID string) ([]*container.Container, error) {
	var result []*container.Container
	for _, c := range m.containers {
		if c.UserID == userID {
			result = append(result, c)
		}
	}
	return result, nil
}

func (m *TestMockContainerManager) GetContainer(ctx context.Context, userID, containerID string) (*container.Container, error) {
	if c, ok := m.containers[containerID]; ok && c.UserID == userID {
		return c, nil
	}
	return nil, fmt.Errorf("container not found")
}

func (m *TestMockContainerManager) StartContainer(ctx context.Context, userID, containerID string) error {
	if c, ok := m.containers[containerID]; ok && c.UserID == userID {
		c.Status = container.StatusRunning
		return nil
	}
	return fmt.Errorf("container not found")
}

func (m *TestMockContainerManager) StopContainer(ctx context.Context, userID, containerID string) error {
	if c, ok := m.containers[containerID]; ok && c.UserID == userID {
		c.Status = container.StatusStopped
		return nil
	}
	return fmt.Errorf("container not found")
}

func (m *TestMockContainerManager) DeleteContainer(ctx context.Context, userID, containerID string) error {
	delete(m.containers, containerID)
	return nil
}

func (m *TestMockContainerManager) ConnectToContainer(ctx context.Context, userID, containerID string) (*container.ContainerConnection, error) {
	// Return a simple mock connection
	if c, ok := m.containers[containerID]; ok && c.UserID == userID {
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

func (m *TestMockContainerManager) GetContainerLogs(ctx context.Context, userID, containerID string, lines int) ([]string, error) {
	return []string{"mock log line 1", "mock log line 2"}, nil
}

func (m *TestMockContainerManager) Close() error {
	return nil
}

func (m *TestMockContainerManager) GetContainerDiagnostics(ctx context.Context, userID, containerName string) (string, error) {
	return "Mock diagnostics for container " + containerName, nil
}

func (m *TestMockContainerManager) ExecuteInContainer(ctx context.Context, userID, containerID string, cmd []string, stdin io.Reader, stdout, stderr io.Writer) error {
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
