package exe

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
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

// TestRealSCPBug tests with actual scp command to reproduce the exact bug the user reported
func TestRealSCPBug(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Create temporary database file
	tmpDB, err := os.CreateTemp("", "real_scp_test_*.db")
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
	email := "real-scp-test@example.com"
	teamName := "realscpteam"
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

	// Create test file to upload
	testContent := "This is a test file for real SCP upload.\nSecond line with newline.\nTotal: 87 bytes.\n"
	testFile, err := os.CreateTemp("", "junk_*.txt")
	if err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}
	defer os.Remove(testFile.Name())

	err = ioutil.WriteFile(testFile.Name(), []byte(testContent), 0644)
	if err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	t.Log("=== Testing Real SCP Upload Bug ===")

	// Check filesystem before SCP upload
	t.Log("Checking filesystem before SCP upload...")
	var beforeStdout strings.Builder
	err = gkeManager.ExecuteInContainer(ctx, fingerprint, testContainer.ID, 
		[]string{"ls", "-la", "/workspace"}, nil, &beforeStdout, nil)
	if err == nil {
		t.Logf("Before SCP upload:\n%s", beforeStdout.String())
	}

	// Use real scp command - this is the exact command the user used
	scpCmd := exec.Command("scp", 
		"-P", portStr,
		"-o", "StrictHostKeyChecking=no", 
		"-o", "UserKnownHostsFile=/dev/null",
		"-i", keyFile.Name(),
		testFile.Name(),
		machineName+"@localhost:~")

	t.Logf("Running SCP command: %s", strings.Join(scpCmd.Args, " "))

	// Run the SCP command
	output, err := scpCmd.CombinedOutput()
	if err != nil {
		t.Logf("SCP command failed: %v", err)
		t.Logf("SCP output: %s", string(output))
	} else {
		t.Log("SCP command completed successfully")
		if len(output) > 0 {
			t.Logf("SCP output: %s", string(output))
		}
	}

	// Wait a moment for any async operations to complete
	time.Sleep(2 * time.Second)

	// Check filesystem after SCP upload
	t.Log("Checking filesystem after SCP upload...")
	var afterStdout strings.Builder
	err = gkeManager.ExecuteInContainer(ctx, fingerprint, testContainer.ID, 
		[]string{"ls", "-la", "/workspace"}, nil, &afterStdout, nil)
	if err != nil {
		t.Errorf("Failed to list container filesystem after SCP: %v", err)
	} else {
		filesystemAfterSCP := afterStdout.String()
		t.Logf("After SCP upload:\n%s", filesystemAfterSCP)

		// Check for the target file
		baseFileName := strings.TrimSuffix(strings.TrimSuffix(testFile.Name(), ".txt"), ".tmp")
		actualTargetName := baseFileName[strings.LastIndex(baseFileName, "/")+1:] + ".txt"
		
		t.Logf("Looking for target file: %s (from %s)", actualTargetName, testFile.Name())

		hasTargetFile := strings.Contains(filesystemAfterSCP, actualTargetName) || 
						 strings.Contains(filesystemAfterSCP, "junk")

		if hasTargetFile {
			t.Log("✅ Target file found in container")
		} else {
			t.Error("❌ Target file NOT found in container")
		}

		// Check for temp files - this is the bug we're looking for
		if strings.Contains(filesystemAfterSCP, ".tmp.") {
			t.Errorf("❌ BUG REPRODUCED: Temp file(s) remain after SCP upload!")
			t.Log("This matches the user's report of files like 'workspace.tmp.1754848873103021000'")
			
			// Show the temp files
			lines := strings.Split(filesystemAfterSCP, "\n")
			for _, line := range lines {
				if strings.Contains(line, ".tmp.") {
					t.Logf("Found temp file: %s", strings.TrimSpace(line))
				}
			}
		} else {
			t.Log("✅ No temp files found after SCP upload")
		}
	}

	t.Log("=== Real SCP Test Complete ===")
}