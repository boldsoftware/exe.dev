package exe

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"exe.dev/container"
	"golang.org/x/crypto/ssh"
)

// TestSSHSCPSFTPIntegration tests real SSH, SCP, and SFTP functionality against actual containers
func TestSSHSCPSFTPIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Create temporary database file
	tmpDB, err := os.CreateTemp("", "test_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp db: %v", err)
	}
	defer os.Remove(tmpDB.Name())
	tmpDB.Close()

	// Create server
	server, err := NewServer(":0", "", ":0", tmpDB.Name(), true, "")
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}
	defer server.Stop()

	// Create GKE manager for testing
	ctx := context.Background()
	gkeManager, err := container.NewGKEManager(ctx, &container.Config{
		ProjectID:            "exe-dev-468515",
		ClusterName:          "exe-cluster",
		ClusterLocation:      "us-central1",
		NamespacePrefix:      "exe-",
		DefaultCPURequest:    "100m",
		DefaultMemoryRequest: "128Mi",
		DefaultStorageSize:   "1Gi",
	})
	if err != nil {
		t.Skipf("Skipping GKE test (no cluster access): %v", err)
	}

	server.containerManager = gkeManager

	// Generate test SSH key and fingerprint
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("Failed to generate private key: %v", err)
	}
	
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatalf("Failed to create signer: %v", err)
	}
	
	// Calculate fingerprint the same way the server does
	hash := sha256.Sum256(signer.PublicKey().Marshal())
	fingerprint := hex.EncodeToString(hash[:])

	// Set up test data
	email := "test@example.com"
	teamName := "sshtestteam"
	machineName := "ssh-test-machine"

	// Create user and team in database
	_, err = server.db.Exec(`INSERT INTO users (public_key_fingerprint, email) VALUES (?, ?)`, fingerprint, email)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	_, err = server.db.Exec(`INSERT INTO teams (name) VALUES (?)`, teamName)
	if err != nil {
		t.Fatalf("Failed to create team: %v", err)
	}

	_, err = server.db.Exec(`INSERT INTO team_members (team_name, user_fingerprint, is_admin) VALUES (?, ?, 1)`, teamName, fingerprint)
	if err != nil {
		t.Fatalf("Failed to add user to team: %v", err)
	}

	// Create test container
	t.Log("Creating test container...")
	containerReq := &container.CreateContainerRequest{
		UserID:   fingerprint,
		Name:     machineName,
		TeamName: teamName,
		Image:    "ubuntu:22.04", // Use standard ubuntu image (likely doesn't have openssh-client)
	}

	testContainer, err := gkeManager.CreateContainer(ctx, containerReq)
	if err != nil {
		t.Fatalf("Failed to create container: %v", err)
	}

	// Store container in database as a machine
	_, err = server.db.Exec(`
		INSERT INTO machines (team_name, name, status, image, container_id, created_by_fingerprint)
		VALUES (?, ?, 'running', ?, ?, ?)
	`, teamName, machineName, containerReq.Image, testContainer.ID, fingerprint)
	if err != nil {
		t.Fatalf("Failed to store machine in database: %v", err)
	}

	// Wait for container to be running
	t.Log("Waiting for container to be running...")
	timeout := time.Now().Add(3 * time.Minute)
	for time.Now().Before(timeout) {
		cont, err := gkeManager.GetContainer(ctx, fingerprint, testContainer.ID)
		if err == nil && cont.Status == container.StatusRunning {
			t.Log("Container is running!")
			break
		}
		if err != nil {
			t.Logf("Error checking container status: %v", err)
		} else {
			t.Logf("Container status: %v, waiting...", cont.Status)
		}
		time.Sleep(5 * time.Second)
	}

	// Set up SSH server
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to create listener: %v", err)
	}
	defer listener.Close()

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go server.handleSSHConnection(conn)
		}
	}()

	// Give server time to start
	time.Sleep(100 * time.Millisecond)

	// Configure SSH client
	clientConfig := &ssh.ClientConfig{
		User: machineName, // This should trigger machine routing
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(signer),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         30 * time.Second,
	}

	t.Log("=== Testing SSH Direct Command Execution ===")
	
	// Test 1: Basic command execution
	client, err := ssh.Dial("tcp", listener.Addr().String(), clientConfig)
	if err != nil {
		t.Fatalf("Failed to connect via SSH: %v", err)
	}

	session, err := client.NewSession()
	if err != nil {
		t.Fatalf("Failed to create SSH session: %v", err)
	}

	// Test a simple command first
	output, err := session.CombinedOutput("echo 'SSH works'")
	session.Close()
	if err != nil {
		t.Fatalf("SSH command failed: %v", err)
	}
	t.Logf("SSH command output: %q", string(output))

	// Test 2: Check if openssh-client is installed
	session, err = client.NewSession()
	if err != nil {
		t.Fatalf("Failed to create SSH session: %v", err)
	}

	output, err = session.CombinedOutput("which scp")
	session.Close()
	if err != nil {
		t.Logf("scp not found (expected): %v", err)
	} else {
		t.Logf("scp found at: %q", string(output))
	}

	t.Log("=== Testing SCP (This should reproduce the 'message too long' error) ===")
	
	// Test 3: SCP command - this should fail with the exact error the user reported
	session, err = client.NewSession()
	if err != nil {
		t.Fatalf("Failed to create SSH session: %v", err)
	}

	var scpStdout, scpStderr bytes.Buffer
	session.Stdout = &scpStdout
	session.Stderr = &scpStderr

	scpCommand := "scp -t /tmp/testfile"
	t.Logf("Executing SCP command: %s", scpCommand)
	
	err = session.Run(scpCommand)
	session.Close()
	
	t.Logf("SCP command exit status: %v", err)
	t.Logf("SCP stdout: %q", scpStdout.String())
	t.Logf("SCP stderr: %q", scpStderr.String())
	
	// Check if we get the protocol-breaking output
	stdoutStr := scpStdout.String()
	if strings.Contains(stdoutStr, "Executed:") || strings.Contains(stdoutStr, "command not found") {
		t.Errorf("❌ SCP PROTOCOL VIOLATION: stdout contains text that breaks SCP protocol: %q", stdoutStr)
		t.Error("This is the root cause of 'Received message too long 1397118032' error")
	}

	t.Log("=== Testing SFTP (This should also reproduce the error) ===")
	
	// Test 4: SFTP subsystem - this should also fail with similar error
	session, err = client.NewSession()
	if err != nil {
		t.Fatalf("Failed to create SSH session: %v", err)
	}

	var sftpStdout, sftpStderr bytes.Buffer
	session.Stdout = &sftpStdout
	session.Stderr = &sftpStderr

	t.Log("Requesting SFTP subsystem...")
	err = session.RequestSubsystem("sftp")
	if err != nil {
		t.Logf("SFTP subsystem request failed: %v", err)
	}
	
	// Give it a moment to respond
	time.Sleep(1 * time.Second)
	session.Close()
	
	t.Logf("SFTP stdout: %q", sftpStdout.String())
	t.Logf("SFTP stderr: %q", sftpStderr.String())
	
	// Check if SFTP also gets protocol-breaking output
	sftpStdoutStr := sftpStdout.String()
	if strings.Contains(sftpStdoutStr, "Executed:") {
		t.Errorf("❌ SFTP PROTOCOL VIOLATION: stdout contains text that breaks SFTP protocol: %q", sftpStdoutStr)
		t.Error("This is the root cause of SFTP 'Received message too long' error")
	}

	client.Close()

	t.Log("=== Integration test complete ===")
	
	// Clean up - delete the container
	t.Log("Cleaning up test container...")
	// The container will be cleaned up by the test environment
}