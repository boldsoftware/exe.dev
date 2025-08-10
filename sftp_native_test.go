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

// TestNativeSFTPIntegration tests the native Go SFTP server with real file operations
func TestNativeSFTPIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Create temporary database file
	tmpDB, err := os.CreateTemp("", "sftp_test_*.db")
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
	email := "sftp-test@example.com"
	teamName := "sftptestteam"
	machineName := "sftp-test-machine"

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
	t.Log("Creating SFTP test container...")
	containerReq := &container.CreateContainerRequest{
		UserID:   fingerprint,
		Name:     machineName,
		TeamName: teamName,
		Image:    "ubuntu:22.04", // Standard ubuntu image
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

	t.Log("=== Testing Native SFTP Server ===")
	
	// Connect via SSH
	sshClient, err := ssh.Dial("tcp", listener.Addr().String(), clientConfig)
	if err != nil {
		t.Fatalf("Failed to connect via SSH: %v", err)
	}
	defer sshClient.Close()

	// Create SFTP client
	sftpClient, err := sftp.NewClient(sshClient)
	if err != nil {
		t.Fatalf("Failed to create SFTP client: %v", err)
	}
	defer sftpClient.Close()

	t.Log("SFTP client connected successfully!")

	// Test 1: List directory contents
	t.Log("1. Testing directory listing...")
	files, err := sftpClient.ReadDir("/workspace")
	if err != nil {
		t.Errorf("Failed to list directory: %v", err)
	} else {
		t.Logf("Directory contains %d entries:", len(files))
		for _, file := range files {
			t.Logf("  %s (%d bytes, %v)", file.Name(), file.Size(), file.Mode())
		}
	}

	// Test 2: Create a test file
	testFileName := "/workspace/sftp_test.txt"
	testContent := "Hello from native SFTP server!\nThis file was created via SFTP.\n"
	
	t.Log("2. Testing file creation...")
	file, err := sftpClient.Create(testFileName)
	if err != nil {
		t.Errorf("Failed to create file: %v", err)
	} else {
		n, err := file.Write([]byte(testContent))
		if err != nil {
			t.Errorf("Failed to write to file: %v", err)
		} else {
			t.Logf("Wrote %d bytes to file", n)
		}
		file.Close()
	}

	// Test 3: Read the file back
	t.Log("3. Testing file reading...")
	file, err = sftpClient.Open(testFileName)
	if err != nil {
		t.Errorf("Failed to open file for reading: %v", err)
	} else {
		buffer := make([]byte, 1024)
		n, err := file.Read(buffer)
		if err != nil && err.Error() != "EOF" {
			t.Errorf("Failed to read from file: %v", err)
		} else {
			readContent := string(buffer[:n])
			t.Logf("Read %d bytes: %q", n, readContent)
			if readContent != testContent {
				t.Errorf("File content mismatch. Expected %q, got %q", testContent, readContent)
			}
		}
		file.Close()
	}

	// Test 4: Stat the file
	t.Log("4. Testing file stat...")
	stat, err := sftpClient.Stat(testFileName)
	if err != nil {
		t.Errorf("Failed to stat file: %v", err)
	} else {
		t.Logf("File stat: name=%s, size=%d, mode=%v, mtime=%v", 
			stat.Name(), stat.Size(), stat.Mode(), stat.ModTime())
	}

	// Test 5: Create a directory
	testDirName := "/workspace/sftp_test_dir"
	t.Log("5. Testing directory creation...")
	err = sftpClient.Mkdir(testDirName)
	if err != nil {
		t.Errorf("Failed to create directory: %v", err)
	} else {
		t.Logf("Created directory: %s", testDirName)
	}

	// Test 6: Rename the file
	newFileName := testDirName + "/renamed_file.txt"
	t.Log("6. Testing file rename...")
	err = sftpClient.Rename(testFileName, newFileName)
	if err != nil {
		t.Errorf("Failed to rename file: %v", err)
	} else {
		t.Logf("Renamed file from %s to %s", testFileName, newFileName)
	}

	// Test 7: Remove the file and directory
	t.Log("7. Testing file/directory removal...")
	err = sftpClient.Remove(newFileName)
	if err != nil {
		t.Errorf("Failed to remove file: %v", err)
	} else {
		t.Logf("Removed file: %s", newFileName)
	}

	// Verify the file operations worked by checking with direct container commands
	t.Log("8. Verifying operations via container exec...")
	
	// Check if the directory was created
	var stdout strings.Builder
	err = gkeManager.ExecuteInContainer(ctx, fingerprint, testContainer.ID, 
		[]string{"ls", "-la", "/workspace"}, nil, &stdout, nil)
	if err != nil {
		t.Errorf("Failed to list workspace: %v", err)
	} else {
		output := stdout.String()
		t.Logf("Workspace contents after SFTP operations:\n%s", output)
		
		if !strings.Contains(output, "sftp_test_dir") {
			t.Errorf("Expected to find sftp_test_dir in workspace")
		}
	}

	t.Log("=== Native SFTP test complete ===")
}