package exe

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// TestSSHCreateAndShellIntegration tests the full SSH flow:
// 1. SSH to server
// 2. Run create command
// 3. Test that the shell works after container is created
func TestSSHCreateAndShellIntegration(t *testing.T) {
	// Create temporary database file
	tmpDB, err := os.CreateTemp("", "test_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp db: %v", err)
	}
	defer os.Remove(tmpDB.Name())
	tmpDB.Close()

	// Create mock container manager
	mockManager := NewMockContainerManager()

	// Create server in dev mode
	server, err := NewServer(":0", "", ":0", tmpDB.Name(), true, "")
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}
	server.containerManager = mockManager
	
	// Start the server
	go server.Start()
	defer server.Stop()
	
	// Wait for server to start and get actual port
	time.Sleep(200 * time.Millisecond)
	
	// The server will bind to a random port when given :0
	// We need to find the actual port it's listening on
	// For testing, let's use a specific port
	server2, err := NewServer(":18080", "", ":12224", tmpDB.Name(), true, "")
	if err != nil {
		t.Fatalf("Failed to create server with specific ports: %v", err)
	}
	server.Stop() // Stop the first server
	server = server2
	server.containerManager = mockManager
	
	go server.Start()
	defer server.Stop()
	time.Sleep(200 * time.Millisecond)
	
	sshAddr := ":12224"
	
	// Generate SSH key for testing
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("Failed to generate RSA key: %v", err)
	}
	
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatalf("Failed to create SSH signer: %v", err)
	}
	
	// Create SSH client config
	config := &ssh.ClientConfig{
		User: "",
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(signer),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	}
	
	// Connect to SSH server
	client, err := ssh.Dial("tcp", "127.0.0.1"+sshAddr, config)
	if err != nil {
		t.Fatalf("Failed to connect to SSH server: %v", err)
	}
	defer client.Close()
	
	// Create a session
	session, err := client.NewSession()
	if err != nil {
		t.Fatalf("Failed to create SSH session: %v", err)
	}
	defer session.Close()
	
	// Set up pipes for stdin/stdout/stderr
	stdin, err := session.StdinPipe()
	if err != nil {
		t.Fatalf("Failed to get stdin pipe: %v", err)
	}
	
	stdout, err := session.StdoutPipe()
	if err != nil {
		t.Fatalf("Failed to get stdout pipe: %v", err)
	}
	
	// Request a PTY
	if err := session.RequestPty("xterm", 80, 24, ssh.TerminalModes{}); err != nil {
		t.Fatalf("Failed to request PTY: %v", err)
	}
	
	// Start a shell
	if err := session.Shell(); err != nil {
		t.Fatalf("Failed to start shell: %v", err)
	}
	
	// Buffer to collect all output
	var allOutput bytes.Buffer
	
	// Helper function to read until we see a pattern
	readUntil := func(reader io.Reader, pattern string, timeout time.Duration) (string, error) {
		result := make(chan string, 1)
		errChan := make(chan error, 1)
		
		go func() {
			buf := make([]byte, 4096)
			var output bytes.Buffer
			
			for {
				n, err := reader.Read(buf)
				if err != nil {
					errChan <- err
					return
				}
				if n > 0 {
					output.Write(buf[:n])
					allOutput.Write(buf[:n]) // Also collect in global buffer
					fullOutput := output.String()
					if strings.Contains(fullOutput, pattern) {
						result <- fullOutput
						return
					}
				}
			}
		}()
		
		select {
		case output := <-result:
			return output, nil
		case err := <-errChan:
			return "", fmt.Errorf("error: %v (output so far: %s)", err, allOutput.String())
		case <-time.After(timeout):
			return "", fmt.Errorf("timeout waiting for pattern: %s (output so far: %s)", pattern, allOutput.String())
		}
	}
	
	// Helper to send a command
	sendCommand := func(cmd string) {
		t.Logf("Sending command: %s", cmd)
		stdin.Write([]byte(cmd + "\n"))
	}
	
	// Wait for initial prompt (might be registration or main menu)
	output, err := readUntil(stdout, ":", 5*time.Second)
	if err != nil {
		t.Fatalf("Failed to read initial output: %v", err)
	}
	t.Logf("Initial output:\n%s", output)
	
	// Check if we need to register
	if strings.Contains(output, "email") || strings.Contains(output, "Email") {
		// Handle registration flow
		t.Log("Handling registration flow...")
		
		// Enter email
		sendCommand("test@example.com")
		
		// Wait for team name prompt
		output, err = readUntil(stdout, "Team name:", 5*time.Second)
		if err != nil {
			t.Fatalf("Failed to read team prompt: %v", err)
		}
		t.Logf("Got team prompt:\n%s", output)
		
		// Enter team name
		sendCommand("testteam")
		
		// Wait for payment setup - look for credit card prompt
		output, err = readUntil(stdout, "Credit card number:", 10*time.Second)
		if err != nil {
			t.Fatalf("Failed to get payment prompt: %v", err)
		}
		t.Logf("Got payment prompt:\n%s", output)
		
		// Enter test credit card
		sendCommand("4242424242424242")
		
		// Wait for completion and main menu - look for the prompt
		output, err = readUntil(stdout, "exe.dev", 10*time.Second)
		if err != nil {
			t.Fatalf("Failed to complete registration and get main prompt: %v", err)
		}
		t.Logf("Registration complete, at main prompt:\n%s", output)
	}
	
	// Now we should be at the main prompt
	t.Log("At main prompt, creating container...")
	
	// Send create command with flags
	sendCommand("create --name=testcontainer --image=ubuntu:22.04")
	
	// Wait for the container to be created - first we should see "Creating" message
	output, err = readUntil(stdout, "Creating", 10*time.Second)
	if err != nil {
		t.Fatalf("Failed to see 'Creating' message: %v", err)
	}
	t.Logf("Container creation started:\n%s", output)
	
	// Wait for "Ready in" message
	output, err = readUntil(stdout, "Ready in", 30*time.Second)
	if err != nil {
		t.Fatalf("Failed to see 'Ready in' message: %v", err)
	}
	t.Logf("Container ready message:\n%s", output)
	
	// After container creation, we should return to the main menu
	// We should see the exe.dev prompt again
	output, err = readUntil(stdout, "exe.dev", 5*time.Second)
	if err != nil {
		t.Fatalf("Failed to return to main menu after container creation: %v", err)
	}
	t.Logf("Back at main menu:\n%s", output)
	
	// Now manually SSH into the container
	t.Log("Now SSHing into the container...")
	sendCommand("ssh testcontainer")
	
	// Wait for the shell prompt
	output, err = readUntil(stdout, "root@", 10*time.Second)
	if err != nil {
		t.Fatalf("Failed to get shell prompt after ssh command: %v", err)
	}
	t.Logf("Got shell prompt after ssh command:\n%s", output)
	
	// Now test that commands actually work in the shell
	t.Log("Testing shell commands...")
	
	// Test 1: ls command
	sendCommand("ls")
	output, err = readUntil(stdout, "file", 5*time.Second)
	if err != nil {
		t.Errorf("Failed to get ls output: %v", err)
	}
	if !strings.Contains(output, "file1.txt") || !strings.Contains(output, "file2.txt") {
		t.Errorf("ls command didn't produce expected output. Got:\n%s", output)
	}
	t.Logf("ls output:\n%s", output)
	
	// Test 2: pwd command
	sendCommand("pwd")
	output, err = readUntil(stdout, "/workspace", 5*time.Second)
	if err != nil {
		t.Errorf("Failed to get pwd output: %v", err)
	}
	if !strings.Contains(output, "/workspace") {
		t.Errorf("pwd command didn't produce expected output. Got:\n%s", output)
	}
	t.Logf("pwd output:\n%s", output)
	
	// Test 3: echo command
	sendCommand("echo hello world")
	output, err = readUntil(stdout, "hello world", 5*time.Second)
	if err != nil {
		t.Errorf("Failed to get echo output: %v", err)
	}
	if !strings.Contains(output, "hello world") {
		t.Errorf("echo command didn't produce expected output. Got:\n%s", output)
	}
	t.Logf("echo output:\n%s", output)
	
	// Test 4: Check that we get a new prompt after each command
	// The prompt should appear again after the command output
	if !strings.Contains(output, "root@") || strings.Count(output, "root@") < 2 {
		t.Errorf("Shell prompt not appearing after commands. Output:\n%s", output)
	}
	
	// Test 5: exit command
	sendCommand("exit")
	output, err = readUntil(stdout, "Connection closed", 5*time.Second)
	if err != nil {
		// Might also just close the connection
		t.Logf("Exit may have closed connection: %v", err)
	} else {
		t.Logf("Exit output:\n%s", output)
	}
}