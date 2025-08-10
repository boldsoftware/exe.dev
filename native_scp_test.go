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
	"golang.org/x/crypto/ssh"
)

// TestNativeSCPFallback tests that SCP commands fall back to native implementation when container lacks scp binary
func TestNativeSCPFallback(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Create temporary database file
	tmpDB, err := os.CreateTemp("", "native_scp_test_*.db")
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
	email := "native-scp-test@example.com"
	teamName := "nativescpteam"
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

	t.Log("=== Testing Native SCP Fallback ===")

	// Connect via SSH
	sshClient, err := ssh.Dial("tcp", listener.Addr().String(), clientConfig)
	if err != nil {
		t.Fatalf("Failed to connect via SSH: %v", err)
	}
	defer sshClient.Close()

	// Check filesystem before SCP operation
	t.Log("Checking filesystem before native SCP test...")
	var beforeStdout strings.Builder
	err = gkeManager.ExecuteInContainer(ctx, fingerprint, testContainer.ID, 
		[]string{"ls", "-la", "/workspace"}, nil, &beforeStdout, nil)
	if err == nil {
		t.Logf("Before native SCP:\n%s", beforeStdout.String())
	}

	// Create SSH session and execute SCP command
	session, err := sshClient.NewSession()
	if err != nil {
		t.Fatalf("Failed to create SSH session: %v", err)
	}
	defer session.Close()

	// Simulate SCP target mode command: "scp -t ~"
	// This should trigger our native SCP implementation when real scp isn't available
	testContent := "This is test content for native SCP.\nSecond line.\n"
	scpCommand := "scp -t ~"

	// Create a pipe for sending SCP protocol data
	stdinPipe, err := session.StdinPipe()
	if err != nil {
		t.Fatalf("Failed to create stdin pipe: %v", err)
	}

	stdoutPipe, err := session.StdoutPipe()
	if err != nil {
		t.Fatalf("Failed to create stdout pipe: %v", err)
	}

	stderrPipe, err := session.StderrPipe()
	if err != nil {
		t.Fatalf("Failed to create stderr pipe: %v", err)
	}

	// Start the SCP command
	err = session.Start(scpCommand)
	if err != nil {
		t.Fatalf("Failed to start SCP command: %v", err)
	}

	t.Log("SCP command started, expecting native implementation to take over...")

	// Read initial response (should be 0 for OK if native SCP is working)
	response := make([]byte, 1)
	_, err = stdoutPipe.Read(response)
	if err != nil {
		// Read stderr to see what happened
		stderrBuf := make([]byte, 1024)
		n, _ := stderrPipe.Read(stderrBuf)
		t.Logf("SCP stderr: %s", string(stderrBuf[:n]))
		t.Fatalf("Failed to read SCP response: %v", err)
	}

	if response[0] != 0 {
		t.Errorf("Expected SCP OK response (0), got %d", response[0])
		// Read stderr for more info
		stderrBuf := make([]byte, 1024)
		n, _ := stderrPipe.Read(stderrBuf)
		t.Logf("SCP stderr: %s", string(stderrBuf[:n]))
	} else {
		t.Log("✅ Native SCP responded with OK - fallback implementation is working!")
	}

	// Send SCP file protocol message
	filename := "native_scp_test.txt"
	scpMessage := fmt.Sprintf("C0644 %d %s\n%s", len(testContent), filename, testContent)
	
	_, err = stdinPipe.Write([]byte(scpMessage))
	if err != nil {
		t.Fatalf("Failed to send SCP file data: %v", err)
	}

	// Read response (should be 0 for OK)
	_, err = stdoutPipe.Read(response)
	if err != nil {
		t.Errorf("Failed to read file transfer response: %v", err)
	} else if response[0] != 0 {
		t.Errorf("Expected file transfer OK (0), got %d", response[0])
	} else {
		t.Log("✅ File transfer acknowledged by native SCP implementation")
	}

	// Close stdin to signal end of transfer
	stdinPipe.Close()

	// Wait for command to complete
	err = session.Wait()
	if err != nil {
		t.Logf("SCP command completed with error: %v", err)
	} else {
		t.Log("✅ SCP command completed successfully")
	}

	// Wait a moment for file system operations to complete
	time.Sleep(2 * time.Second)

	// Check filesystem after SCP operation
	t.Log("Checking filesystem after native SCP...")
	var afterStdout strings.Builder
	err = gkeManager.ExecuteInContainer(ctx, fingerprint, testContainer.ID, 
		[]string{"ls", "-la", "/workspace"}, nil, &afterStdout, nil)
	if err != nil {
		t.Errorf("Failed to list container filesystem after native SCP: %v", err)
	} else {
		filesystemAfterSCP := afterStdout.String()
		t.Logf("After native SCP:\n%s", filesystemAfterSCP)

		// Check for the target file
		if strings.Contains(filesystemAfterSCP, filename) {
			t.Log("✅ Target file found - native SCP implementation created the file correctly!")
		} else {
			t.Error("❌ Target file NOT found - native SCP implementation failed")
		}

		// Check for temp files - this was the original bug
		if strings.Contains(filesystemAfterSCP, ".tmp.") {
			t.Errorf("❌ BUG: Temp file(s) remain after native SCP!")
			
			// Show the temp files
			lines := strings.Split(filesystemAfterSCP, "\n")
			for _, line := range lines {
				if strings.Contains(line, ".tmp.") {
					t.Logf("Found temp file: %s", strings.TrimSpace(line))
				}
			}
		} else {
			t.Log("✅ No temp files found - native SCP implementation cleaned up properly!")
		}
	}

	// Verify file content
	var contentStdout strings.Builder
	err = gkeManager.ExecuteInContainer(ctx, fingerprint, testContainer.ID, 
		[]string{"cat", "/workspace/" + filename}, nil, &contentStdout, nil)
	if err != nil {
		t.Errorf("Could not read file content: %v", err)
	} else {
		readContent := contentStdout.String()
		if readContent == testContent {
			t.Log("✅ File content matches - native SCP preserved file integrity!")
		} else {
			t.Errorf("❌ File content mismatch. Expected %q, got %q", testContent, readContent)
		}
	}

	t.Log("=== Native SCP Fallback Test Complete ===")
}