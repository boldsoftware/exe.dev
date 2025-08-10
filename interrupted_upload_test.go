package exe

import (
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
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// TestInterruptedUpload tests what happens when an SFTP upload is interrupted before Close()
func TestInterruptedUpload(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Create temporary database file
	tmpDB, err := os.CreateTemp("", "interrupted_upload_test_*.db")
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
	email := "interrupted-upload-test@example.com"
	teamName := "interruptteam"
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

	t.Log("=== Testing Interrupted Upload (Simulates SCP Bug) ===")
	
	// Connect via SSH
	sshClient, err := ssh.Dial("tcp", listener.Addr().String(), clientConfig)
	if err != nil {
		t.Fatalf("Failed to connect via SSH: %v", err)
	}
	defer sshClient.Close()

	sftpClient, err := sftp.NewClient(sshClient)
	if err != nil {
		t.Fatalf("Failed to create SFTP client: %v", err)
	}
	// DON'T defer sftpClient.Close() - we want to simulate an interrupted connection

	// Create test file
	testContent := "This upload will be interrupted before Close().\nLeaving a temp file behind.\n"
	targetFilename := "interrupted.txt"
	targetPath := "/" + targetFilename

	t.Logf("Starting upload of: %s", targetPath)

	// Step 1: Create file (this creates a ContainerWriterAt and temp file)
	file, err := sftpClient.Create(targetPath)
	if err != nil {
		t.Fatalf("Failed to create file for upload: %v", err)
	}

	// Step 2: Write to the file (this buffers data but doesn't commit yet)
	n, err := file.Write([]byte(testContent))
	if err != nil {
		t.Fatalf("Failed to write file content: %v", err)
	}
	t.Logf("Wrote %d bytes to file", n)

	// Step 3: Check filesystem after write - should show temp file now
	var afterWriteStdout strings.Builder
	err = gkeManager.ExecuteInContainer(ctx, fingerprint, testContainer.ID, 
		[]string{"ls", "-la", "/workspace"}, nil, &afterWriteStdout, nil)
	if err == nil {
		filesystemAfterWrite := afterWriteStdout.String()
		t.Logf("After file write (before close):\n%s", filesystemAfterWrite)
		
		if strings.Contains(filesystemAfterWrite, ".tmp.") {
			t.Log("✅ Temp file created as expected during write")
		} else {
			t.Log("⚠️  No temp file visible after write - this is unexpected")
		}
	}

	// Step 4: SIMULATE INTERRUPTION - close connections WITHOUT calling file.Close()
	t.Log("🔥 SIMULATING CONNECTION INTERRUPTION - closing without file.Close()")
	
	// Close the SFTP client abruptly (simulates network interruption or SCP crash)
	sftpClient.Close()
	
	// Close the SSH connection too
	sshClient.Close()
	
	// DON'T call file.Close() - this simulates the interruption

	// Step 5: Wait a moment then check filesystem - temp files should remain (THE BUG)
	time.Sleep(2 * time.Second)
	
	t.Log("Checking filesystem after connection interruption...")
	var afterInterruptStdout strings.Builder
	err = gkeManager.ExecuteInContainer(ctx, fingerprint, testContainer.ID, 
		[]string{"ls", "-la", "/workspace"}, nil, &afterInterruptStdout, nil)
	if err != nil {
		t.Errorf("Failed to list container filesystem after interruption: %v", err)
	} else {
		filesystemAfterInterrupt := afterInterruptStdout.String()
		t.Logf("After connection interruption:\n%s", filesystemAfterInterrupt)

		// Check results - this should demonstrate the bug
		if strings.Contains(filesystemAfterInterrupt, targetFilename) {
			t.Errorf("❌ Unexpected: Target file found even though Close() was never called")
		} else {
			t.Log("✅ Expected: Target file not found (Close() was never called)")
		}

		if strings.Contains(filesystemAfterInterrupt, ".tmp.") {
			t.Errorf("❌ BUG REPRODUCED: Temp file(s) remain after connection interruption!")
			t.Log("This is the exact bug the user reported - SCP interruptions leave temp files behind")
			
			// Show the temp files
			lines := strings.Split(filesystemAfterInterrupt, "\n")
			for _, line := range lines {
				if strings.Contains(line, ".tmp.") {
					t.Logf("Remaining temp file: %s", strings.TrimSpace(line))
				}
			}
		} else {
			t.Log("✅ No temp files found - either temp file was cleaned up or never created")
		}
	}

	t.Log("=== Interrupted Upload Test Complete ===")
	t.Log("Result: This test demonstrates that temp files remain when connections are interrupted")
	t.Log("Solution: Implement cleanup mechanism to remove abandoned temp files")
}