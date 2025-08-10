package exe

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"exe.dev/container"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// TestTempFileCleanup specifically tests that temp files are cleaned up properly
func TestTempFileCleanup(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Create temporary database file
	tmpDB, err := os.CreateTemp("", "temp_cleanup_test_*.db")
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

	// Generate SSH key and fingerprint
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("Failed to generate private key: %v", err)
	}
	
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatalf("Failed to create signer: %v", err)
	}
	
	hash := sha256.Sum256(signer.PublicKey().Marshal())
	fingerprint := hex.EncodeToString(hash[:])

	// Set up test data
	email := "temp-cleanup-test@example.com"
	teamName := "tempcleanteam"
	machineName := "delta-dog"

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
		Image:    "ubuntu:22.04",
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

	// First, create some temp files manually to test cleanup
	t.Log("Creating temp files manually to test cleanup...")
	tempFileName1 := fmt.Sprintf("workspace.tmp.%d", time.Now().UnixNano())
	tempFileName2 := fmt.Sprintf("workspace.tmp.%d", time.Now().UnixNano()+1)

	// Create temp files in container
	var tmpOut strings.Builder
	err = gkeManager.ExecuteInContainer(ctx, fingerprint, testContainer.ID, 
		[]string{"sh", "-c", fmt.Sprintf("echo 'temp content 1' > /workspace/%s", tempFileName1)}, nil, &tmpOut, nil)
	if err != nil {
		t.Fatalf("Failed to create temp file 1: %v", err)
	}

	err = gkeManager.ExecuteInContainer(ctx, fingerprint, testContainer.ID, 
		[]string{"sh", "-c", fmt.Sprintf("echo 'temp content 2' > /workspace/%s", tempFileName2)}, nil, &tmpOut, nil)
	if err != nil {
		t.Fatalf("Failed to create temp file 2: %v", err)
	}

	// Verify temp files exist
	var beforeStdout strings.Builder
	err = gkeManager.ExecuteInContainer(ctx, fingerprint, testContainer.ID, 
		[]string{"ls", "-la", "/workspace"}, nil, &beforeStdout, nil)
	if err != nil {
		t.Fatalf("Failed to list workspace before cleanup: %v", err)
	}

	beforeContents := beforeStdout.String()
	t.Logf("Workspace before cleanup:\n%s", beforeContents)

	if !strings.Contains(beforeContents, tempFileName1) || !strings.Contains(beforeContents, tempFileName2) {
		t.Fatalf("Temp files were not created successfully")
	}

	t.Log("✅ Temp files created successfully")

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
		User: machineName,
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(signer),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         30 * time.Second,
	}

	t.Log("=== Testing Temp File Cleanup ===")

	// Connect via SSH and create SFTP client
	sshClient, err := ssh.Dial("tcp", listener.Addr().String(), clientConfig)
	if err != nil {
		t.Fatalf("Failed to connect via SSH: %v", err)
	}
	defer sshClient.Close()

	// IMPORTANT: Creating a new SFTP client should trigger cleanup of existing temp files
	t.Log("Creating SFTP client - this should trigger temp file cleanup...")
	sftpClient, err := sftp.NewClient(sshClient)
	if err != nil {
		t.Fatalf("Failed to create SFTP client: %v", err)
	}
	defer sftpClient.Close()

	// Wait a moment for cleanup to happen
	time.Sleep(2 * time.Second)

	// Check if temp files were cleaned up
	var afterCleanupStdout strings.Builder
	err = gkeManager.ExecuteInContainer(ctx, fingerprint, testContainer.ID, 
		[]string{"ls", "-la", "/workspace"}, nil, &afterCleanupStdout, nil)
	if err != nil {
		t.Fatalf("Failed to list workspace after cleanup: %v", err)
	}

	afterCleanupContents := afterCleanupStdout.String()
	t.Logf("Workspace after SFTP connection (should trigger cleanup):\n%s", afterCleanupContents)

	// Check if temp files were removed
	if strings.Contains(afterCleanupContents, tempFileName1) || strings.Contains(afterCleanupContents, tempFileName2) {
		t.Errorf("❌ CLEANUP FAILED: Temp files still exist after SFTP connection")
		t.Errorf("Expected temp files %s and %s to be cleaned up", tempFileName1, tempFileName2)
	} else {
		t.Log("✅ CLEANUP SUCCESS: Temp files were removed when SFTP connection was established")
	}

	// Now test the complete cycle: create file via SFTP and ensure no temp files remain
	t.Log("Testing complete SFTP upload cycle...")

	testContent := "This is a test file to verify no temp files remain after upload.\n"
	testFileName := "cleanup_test_file.txt"

	// Create file via SFTP
	file, err := sftpClient.Create("/" + testFileName)
	if err != nil {
		t.Fatalf("Failed to create SFTP file: %v", err)
	}

	_, err = file.Write([]byte(testContent))
	if err != nil {
		t.Fatalf("Failed to write SFTP file content: %v", err)
	}

	err = file.Close()
	if err != nil {
		t.Fatalf("Failed to close SFTP file: %v", err)
	}

	// Wait a moment for file operations to complete
	time.Sleep(1 * time.Second)

	// Check final filesystem state
	var finalStdout strings.Builder
	err = gkeManager.ExecuteInContainer(ctx, fingerprint, testContainer.ID, 
		[]string{"ls", "-la", "/workspace"}, nil, &finalStdout, nil)
	if err != nil {
		t.Fatalf("Failed to list workspace after SFTP upload: %v", err)
	}

	finalContents := finalStdout.String()
	t.Logf("Final workspace contents:\n%s", finalContents)

	// Check results
	if !strings.Contains(finalContents, testFileName) {
		t.Errorf("❌ Target file %s not found after SFTP upload", testFileName)
	} else {
		t.Log("✅ Target file created successfully")
	}

	if strings.Contains(finalContents, ".tmp.") {
		t.Errorf("❌ BUG: Temp files remain after SFTP upload!")
		// Show temp files
		lines := strings.Split(finalContents, "\n")
		for _, line := range lines {
			if strings.Contains(line, ".tmp.") {
				t.Logf("Found temp file: %s", strings.TrimSpace(line))
			}
		}
	} else {
		t.Log("✅ No temp files remain after SFTP upload - atomic operations working correctly!")
	}

	t.Log("=== Temp File Cleanup Test Complete ===")
}