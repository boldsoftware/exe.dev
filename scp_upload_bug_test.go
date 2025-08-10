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

// TestSCPUploadBug reproduces the exact bug where SCP uploads create temp files but don't rename them
func TestSCPUploadBug(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Create temporary database file
	tmpDB, err := os.CreateTemp("", "scp_bug_test_*.db")
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
	email := "scp-bug-test@example.com"
	teamName := "scpbugteam"
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

	t.Log("=== Testing SCP Upload Bug ===")
	
	// Connect via SSH and create SFTP client
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

	// Create a test file with specific content (like junk.txt)
	testContent := "This is a test file for SCP upload bug reproduction.\nIt contains multiple lines.\nTotal: 87 bytes.\n"
	targetFilename := "junk.txt"
	targetPath := "/" + targetFilename // SCP uploads to root of SFTP filesystem

	t.Logf("Uploading file with %d bytes to %s", len(testContent), targetPath)

	// Upload the file (this simulates what SCP does)
	file, err := sftpClient.Create(targetPath)
	if err != nil {
		t.Fatalf("Failed to create file for upload: %v", err)
	}

	n, err := file.Write([]byte(testContent))
	if err != nil {
		t.Fatalf("Failed to write file content: %v", err)
	}
	t.Logf("Wrote %d bytes to file", n)

	// Close the file (this should trigger the atomic rename)
	err = file.Close()
	if err != nil {
		t.Fatalf("Failed to close file: %v", err)
	}

	t.Log("File upload completed. Checking container filesystem...")

	// Now check what's actually in the container filesystem
	var stdout strings.Builder
	err = gkeManager.ExecuteInContainer(ctx, fingerprint, testContainer.ID, 
		[]string{"ls", "-la", "/workspace"}, nil, &stdout, nil)
	if err != nil {
		t.Fatalf("Failed to list container filesystem: %v", err)
	}

	filesystemContents := stdout.String()
	t.Logf("Container filesystem contents:\n%s", filesystemContents)

	// Check for the expected target file
	if !strings.Contains(filesystemContents, targetFilename) {
		t.Errorf("❌ BUG REPRODUCED: Target file '%s' not found in container filesystem", targetFilename)
		
		// Check for temp files (the bug)
		if strings.Contains(filesystemContents, ".tmp.") {
			t.Errorf("❌ BUG CONFIRMED: Found temp file(s) instead of target file - atomic rename failed")
			
			// Show the temp files
			lines := strings.Split(filesystemContents, "\n")
			for _, line := range lines {
				if strings.Contains(line, ".tmp.") {
					t.Logf("Found temp file: %s", strings.TrimSpace(line))
				}
			}
		}
	} else {
		t.Logf("✅ File upload successful: %s found in container", targetFilename)
	}

	// Verify the file content if it exists
	var contentStdout strings.Builder
	err = gkeManager.ExecuteInContainer(ctx, fingerprint, testContainer.ID, 
		[]string{"cat", "/workspace/" + targetFilename}, nil, &contentStdout, nil)
	if err != nil {
		t.Logf("Could not read target file content (expected if rename failed): %v", err)
		
		// Try reading temp files to verify content was written correctly
		tempFiles := extractTempFileNames(filesystemContents)
		for _, tempFile := range tempFiles {
			var tempStdout strings.Builder
			err = gkeManager.ExecuteInContainer(ctx, fingerprint, testContainer.ID, 
				[]string{"cat", "/workspace/" + tempFile}, nil, &tempStdout, nil)
			if err == nil {
				readContent := tempStdout.String()
				t.Logf("Content of temp file %s: %q", tempFile, readContent)
				if readContent == testContent {
					t.Log("✅ File content is correct in temp file - only rename is failing")
				} else {
					t.Errorf("❌ File content mismatch in temp file. Expected %q, got %q", testContent, readContent)
				}
				break
			}
		}
	} else {
		readContent := contentStdout.String()
		if readContent == testContent {
			t.Log("✅ File content matches expected content")
		} else {
			t.Errorf("❌ File content mismatch. Expected %q, got %q", testContent, readContent)
		}
	}

	t.Log("=== SCP Upload Bug Test Complete ===")
}

// extractTempFileNames extracts .tmp. filenames from ls output
func extractTempFileNames(lsOutput string) []string {
	var tempFiles []string
	lines := strings.Split(lsOutput, "\n")
	for _, line := range lines {
		if strings.Contains(line, ".tmp.") {
			fields := strings.Fields(line)
			if len(fields) > 0 {
				filename := fields[len(fields)-1] // Last field is filename
				if strings.Contains(filename, ".tmp.") {
					tempFiles = append(tempFiles, filename)
				}
			}
		}
	}
	return tempFiles
}