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

// TestDirectoryUpload tests what happens when SFTP client tries to create a file with directory path
func TestDirectoryUpload(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Create mock container manager
	mockManager := NewMockContainerManager()
	
	// Create temporary database
	tmpDB, err := os.CreateTemp("", "directory_upload_*.db")
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
	email := "directory-upload@example.com"
	teamName := "directoryteam"
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

	t.Log("=== Testing Directory Upload Bug ===")

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

	// Test what happens when we try to create a file with just the directory path
	testContent := "This should reproduce the temp file bug.\n"
	
	t.Log("=== Test: Create file with directory path (reproducing SCP behavior) ===")
	
	// This reproduces what might be happening with SCP:
	// SCP requests upload to "~" (directory) but expects it to work like a file
	file, err := sftpClient.Create("/")  // Try to create file at root path
	if err != nil {
		t.Logf("Failed to create file at root path '/': %v", err)
	} else {
		t.Log("⚠️  Successfully created file handle for root path '/'")
		
		_, err = file.Write([]byte(testContent))
		if err != nil {
			t.Logf("Failed to write to root path file: %v", err)
		} else {
			t.Log("⚠️  Successfully wrote to root path file")
		}
		
		err = file.Close()
		if err != nil {
			t.Logf("Failed to close root path file: %v", err)
		} else {
			t.Log("⚠️  Successfully closed root path file")
		}
	}

	// Test with home directory path
	file, err = sftpClient.Create("~")  // This is probably what SCP does
	if err != nil {
		t.Logf("Failed to create file at home path '~': %v", err)
	} else {
		t.Log("❌ BUG REPRODUCTION: Successfully created file handle for directory path '~'")
		
		_, err = file.Write([]byte(testContent))
		if err != nil {
			t.Logf("Failed to write to home path file: %v", err)
		} else {
			t.Log("❌ Successfully wrote to directory path file")
		}
		
		err = file.Close()
		if err != nil {
			t.Logf("❌ Failed to close directory path file: %v (THIS IS THE BUG)", err)
		} else {
			t.Log("⚠️  Successfully closed directory path file")
		}
	}

	// Check what commands were executed
	execCalls := mockManager.GetExecCalls()
	t.Logf("Mock container executed %d commands:", len(execCalls))
	for i, call := range execCalls {
		t.Logf("  %d: %v", i+1, call.Command)
		
		// Look for the problematic mv command
		if len(call.Command) >= 3 && call.Command[0] == "mv" {
			t.Logf("     RENAME: %s → %s", call.Command[1], call.Command[2])
			
			// Check if this is the buggy rename
			if strings.Contains(call.Command[1], ".tmp.") && !strings.Contains(call.Command[2], ".tmp.") {
				if call.Command[2] == "/workspace" {
					t.Logf("     ❌ BUG: Trying to move temp file to directory!")
				}
			}
		}
	}

	t.Log("=== Directory Upload Test Complete ===")
}