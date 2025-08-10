package exe

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"io/ioutil"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"exe.dev/container"
	"golang.org/x/crypto/ssh"
)

// TestRealSCPFilenamePreservation tests that SCP uploads preserve the original filename
// This reproduces the exact bug the user reported: scp junk.txt user@localhost:~ results in "uploaded_file"
func TestRealSCPFilenamePreservation(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Check if scp command is available on the system
	_, err := exec.LookPath("scp")
	if err != nil {
		t.Skip("scp command not available on this system")
	}

	// Create temporary database file
	tmpDB, err := os.CreateTemp("", "real_scp_filename_test_*.db")
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
	email := "real-scp-filename@example.com"
	teamName := "scpfilenameteam"
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

	// Get the actual port number
	_, portStr, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatalf("Failed to get port: %v", err)
	}

	// Create SSH private key file for scp command
	keyFile, err := os.CreateTemp("", "ssh_key_*")
	if err != nil {
		t.Fatalf("Failed to create temp key file: %v", err)
	}
	defer os.Remove(keyFile.Name())

	// Write private key in PEM format
	privateKeyBytes := x509.MarshalPKCS1PrivateKey(privateKey)
	privateKeyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: privateKeyBytes,
	})
	
	err = os.WriteFile(keyFile.Name(), privateKeyPEM, 0600)
	if err != nil {
		t.Fatalf("Failed to write private key: %v", err)
	}

	// Create test file to upload with known content
	testContent := "This is a test file for real SCP upload.\nTotal: 87 bytes padding to make it exactly 87 bytes.\n"
	
	// Create file with exactly the name "junk.txt" to match user's example
	junkPath := "/tmp/junk.txt"
	os.Remove(junkPath) // Remove if exists
	
	err = ioutil.WriteFile(junkPath, []byte(testContent), 0644)
	if err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}
	defer os.Remove(junkPath)

	t.Log("=== Testing Real SCP Upload with Filename Preservation ===")

	// Use real scp command - this reproduces the user's exact command
	scpCmd := exec.Command("scp", 
		"-P", portStr,
		"-o", "StrictHostKeyChecking=no", 
		"-o", "UserKnownHostsFile=/dev/null",
		"-i", keyFile.Name(),
		junkPath,
		fmt.Sprintf("%s@localhost:~", machineName))

	t.Logf("Running SCP command: %s", strings.Join(scpCmd.Args, " "))

	// Run the SCP command
	output, err := scpCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("SCP command failed: %v\nOutput: %s", err, string(output))
	}
	
	t.Log("SCP upload completed successfully")
	if len(output) > 0 {
		t.Logf("SCP output: %s", string(output))
	}

	// Wait a moment for any async operations to complete
	time.Sleep(2 * time.Second)

	// Now use SSH to check the uploaded file - this should reproduce the user's exact command
	sshCmd := exec.Command("ssh",
		"-p", portStr,
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-i", keyFile.Name(),
		fmt.Sprintf("%s@localhost", machineName),
		"ls")

	lsOutput, err := sshCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("SSH ls command failed: %v\nOutput: %s", err, string(lsOutput))
	}

	t.Logf("Directory listing:\n%s", string(lsOutput))

	// Check for the BUG: file should be named "junk.txt", not "uploaded_file"
	if strings.Contains(string(lsOutput), "uploaded_file") {
		t.Errorf("❌ BUG CONFIRMED: File was renamed to 'uploaded_file' instead of keeping original name 'junk.txt'")
	}

	if !strings.Contains(string(lsOutput), "junk.txt") {
		t.Errorf("❌ BUG CONFIRMED: Original filename 'junk.txt' not found in listing")
	} else {
		t.Log("✅ File 'junk.txt' found with correct name")
	}

	// Let's also check with ls -la for more details
	sshCmd2 := exec.Command("ssh",
		"-p", portStr,
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-i", keyFile.Name(),
		fmt.Sprintf("%s@localhost", machineName),
		"ls -la")

	lsLaOutput, err := sshCmd2.CombinedOutput()
	if err != nil {
		t.Logf("SSH ls -la command failed: %v\nOutput: %s", err, string(lsLaOutput))
	} else {
		t.Logf("Detailed directory listing:\n%s", string(lsLaOutput))
	}

	// Verify content of the file
	catCmd := exec.Command("ssh",
		"-p", portStr,
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-i", keyFile.Name(),
		fmt.Sprintf("%s@localhost", machineName),
		"cat junk.txt || cat uploaded_file")

	catOutput, err := catCmd.CombinedOutput()
	if err != nil {
		t.Logf("SSH cat command output: %s", string(catOutput))
		// Don't fail here, we want to see what happened
	}

	if strings.TrimSpace(string(catOutput)) == strings.TrimSpace(testContent) {
		t.Log("✅ File content matches expected content")
	} else {
		t.Errorf("❌ File content mismatch.\nExpected:\n%s\nGot:\n%s", testContent, string(catOutput))
	}

	t.Log("=== Test Complete ===")
}