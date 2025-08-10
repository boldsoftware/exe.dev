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

// TestDebugCloseCall tests if Close() is actually being called on SFTP file uploads
func TestDebugCloseCall(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Create temporary database file
	tmpDB, err := os.CreateTemp("", "debug_close_test_*.db")
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
	email := "debug-close-test@example.com"
	teamName := "debugteam"
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

	t.Log("=== Testing SFTP File Close Debug ===" )
	
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
	defer sftpClient.Close()

	// Create test file
	testContent := "This is debug test content.\nSecond line.\n"
	targetFilename := "debug_test.txt"
	targetPath := "/" + targetFilename

	t.Logf("Creating file: %s", targetPath)

	// Step 1: Check filesystem before file creation
	var beforeStdout strings.Builder
	err = gkeManager.ExecuteInContainer(ctx, fingerprint, testContainer.ID, 
		[]string{"ls", "-la", "/workspace"}, nil, &beforeStdout, nil)
	if err == nil {
		t.Logf("Before file creation:\n%s", beforeStdout.String())
	}

	// Step 2: Create file (this creates a ContainerWriterAt)
	file, err := sftpClient.Create(targetPath)
	if err != nil {
		t.Fatalf("Failed to create file for upload: %v", err)
	}

	t.Log("File created, checking for temp files...")

	// Step 3: Check filesystem after file creation (should show temp file)
	var afterCreateStdout strings.Builder
	err = gkeManager.ExecuteInContainer(ctx, fingerprint, testContainer.ID, 
		[]string{"ls", "-la", "/workspace"}, nil, &afterCreateStdout, nil)
	if err == nil {
		filesystemAfterCreate := afterCreateStdout.String()
		t.Logf("After file creation:\n%s", filesystemAfterCreate)
		
		if strings.Contains(filesystemAfterCreate, ".tmp.") {
			t.Log("✅ Temp file created as expected")
		} else {
			t.Log("⚠️  No temp file visible yet")
		}
	}

	// Step 4: Write to the file
	n, err := file.Write([]byte(testContent))
	if err != nil {
		t.Fatalf("Failed to write file content: %v", err)
	}
	t.Logf("Wrote %d bytes to file", n)

	// Step 5: Check filesystem after write (temp file should have content)
	var afterWriteStdout strings.Builder
	err = gkeManager.ExecuteInContainer(ctx, fingerprint, testContainer.ID, 
		[]string{"ls", "-la", "/workspace"}, nil, &afterWriteStdout, nil)
	if err == nil {
		filesystemAfterWrite := afterWriteStdout.String()
		t.Logf("After file write:\n%s", filesystemAfterWrite)
	}

	// Step 6: Close the file - THIS SHOULD TRIGGER THE ATOMIC RENAME
	t.Log("Closing file - this should trigger atomic rename...")
	err = file.Close()
	if err != nil {
		t.Errorf("Failed to close file: %v", err)
	} else {
		t.Log("✅ File.Close() completed without error")
	}

	// Step 7: Check filesystem after close (temp file should be gone, target file should exist)
	time.Sleep(100 * time.Millisecond) // Give it a moment
	var afterCloseStdout strings.Builder
	err = gkeManager.ExecuteInContainer(ctx, fingerprint, testContainer.ID, 
		[]string{"ls", "-la", "/workspace"}, nil, &afterCloseStdout, nil)
	if err != nil {
		t.Errorf("Failed to list container filesystem after close: %v", err)
	} else {
		filesystemAfterClose := afterCloseStdout.String()
		t.Logf("After file close:\n%s", filesystemAfterClose)

		// Check results
		if strings.Contains(filesystemAfterClose, targetFilename) {
			t.Log("✅ Target file found after close")
		} else {
			t.Errorf("❌ Target file NOT found after close")
		}

		if strings.Contains(filesystemAfterClose, ".tmp.") {
			t.Errorf("❌ BUG CONFIRMED: Temp file(s) still present after close - atomic rename failed")
			
			// Show the temp files
			lines := strings.Split(filesystemAfterClose, "\n")
			for _, line := range lines {
				if strings.Contains(line, ".tmp.") {
					t.Logf("Remaining temp file: %s", strings.TrimSpace(line))
				}
			}
		} else {
			t.Log("✅ No temp files remaining after close")
		}
	}

	// Step 8: Verify file content
	var contentStdout strings.Builder
	err = gkeManager.ExecuteInContainer(ctx, fingerprint, testContainer.ID, 
		[]string{"cat", "/workspace/" + targetFilename}, nil, &contentStdout, nil)
	if err != nil {
		t.Errorf("Could not read target file content: %v", err)
	} else {
		readContent := contentStdout.String()
		t.Logf("File content: %q", readContent)
		if readContent == testContent {
			t.Log("✅ File content matches expected content")
		} else {
			t.Errorf("❌ File content mismatch. Expected %q, got %q", testContent, readContent)
		}
	}

	t.Log("=== Debug Close Test Complete ===")
}