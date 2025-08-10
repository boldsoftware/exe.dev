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

// TestDebugSFTPRequests traces what SFTP requests are made during file upload
func TestDebugSFTPRequests(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Create mock container manager for simpler testing
	mockManager := NewMockContainerManager()
	
	// Create temporary database
	tmpDB, err := os.CreateTemp("", "debug_sftp_*.db")
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
	
	server.containerManager = mockManager

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
	email := "debug-sftp@example.com"
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

	// Create mock container
	containerReq := &container.CreateContainerRequest{
		UserID:   fingerprint,
		Name:     machineName,
		TeamName: teamName,
		Image:    "ubuntu:22.04",
	}

	testContainer, err := mockManager.CreateContainer(context.Background(), containerReq)
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

	t.Log("=== Testing SFTP Requests Debug ===")

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

	t.Log("SFTP client connected successfully")

	// Test 1: Upload file to root directory (like SCP does to ~)
	testContent := "This is test content for debugging SFTP requests.\n"
	
	t.Log("=== Test 1: Upload to root directory (simulating scp file.txt user@host:~) ===")
	
	// This simulates what SCP does when uploading to ~
	file, err := sftpClient.Create("/junk.txt")  // SCP uploads to ~/filename
	if err != nil {
		t.Errorf("Failed to create file: %v", err)
	} else {
		t.Logf("Created file handle for: /junk.txt")
		
		_, err = file.Write([]byte(testContent))
		if err != nil {
			t.Errorf("Failed to write file: %v", err)
		} else {
			t.Log("Wrote file content")
		}
		
		err = file.Close()
		if err != nil {
			t.Errorf("Failed to close file: %v", err)
		} else {
			t.Log("Closed file successfully")
		}
	}

	// Check what commands were executed in the mock container
	execCalls := mockManager.GetExecCalls()
	t.Logf("Mock container executed %d commands:", len(execCalls))
	for i, call := range execCalls {
		t.Logf("  %d: %v -> %q", i+1, call.Command, call.Output)
	}

	// Test 2: Check what temp files would be created
	t.Log("=== Test 2: Check for temp files pattern ===")
	
	// Look for any commands that might reveal temp file names
	for _, call := range execCalls {
		if len(call.Command) > 0 && (call.Command[0] == "find" || strings.Contains(strings.Join(call.Command, " "), "tmp")) {
			t.Logf("Temp-related command: %v", call.Command)
		}
	}

	t.Log("=== SFTP Debug Test Complete ===")
}