package sshproxy

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// TestSCPTildeBug demonstrates the exact bug where scp to ~ fails with "close remote: Failure"
// This test replicates the production failure exactly
func TestSCPTildeBug(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	// Check if Docker is available
	if err := exec.Command("docker", "version").Run(); err != nil {
		t.Skip("Docker not available")
	}

	// Create and start Ubuntu container (matching production)
	containerID, cleanup := setupUbuntuContainer(t)
	defer cleanup()

	// Start SSH server that mimics production setup
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	// Generate SSH host key
	hostKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	hostSigner, err := ssh.NewSignerFromKey(hostKey)
	if err != nil {
		t.Fatal(err)
	}

	// Configure SSH server (matching production)
	config := &ssh.ServerConfig{
		NoClientAuth: true, // Accept any client
	}
	config.AddHostKey(hostSigner)

	// Create container manager
	manager := &DockerContainerManager{containerID: containerID}

	// Accept connections
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}

			go handleProductionLikeSSHConnection(t, conn, config, manager, containerID)
		}
	}()

	// Give server time to start
	time.Sleep(100 * time.Millisecond)

	// Create test file
	testFile := filepath.Join(t.TempDir(), "junk.txt")
	testContent := []byte("This is test content that should be uploaded")
	if err := os.WriteFile(testFile, testContent, 0644); err != nil {
		t.Fatal(err)
	}

	// Get port
	addr := listener.Addr().String()
	parts := strings.Split(addr, ":")
	port := parts[len(parts)-1]

	t.Run("SCPToTilde", func(t *testing.T) {
		// This is the EXACT command that fails in production
		cmd := scpCommand(
			"-v", // Add verbose output
			"-P", port,
			testFile,
			"test@localhost:~")

		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr

		err := cmd.Run()
		stderrOutput := stderr.String()
		stdoutOutput := stdout.String()

		// With the fix, this should now succeed
		if err == nil {
			t.Log("✓ SCP to ~ succeeded (fix is working!)")
			// Check if file was created
			var checkOut bytes.Buffer
			checkCmd := exec.Command("docker", "exec", containerID, "ls", "-la", "/workspace")
			checkCmd.Stdout = &checkOut
			checkCmd.Run()
			t.Logf("After successful upload, /workspace contains: %s", checkOut.String())
		} else {
			// The bug is fixed, so SCP should succeed
			t.Errorf("SCP failed when it should succeed (fix not working): %v", err)
			t.Logf("stderr: %s", stderrOutput)
			if stdoutOutput != "" {
				t.Logf("stdout: %s", stdoutOutput)
			}
		}
	})

	t.Run("SCPToExplicitPath", func(t *testing.T) {
		// Test with explicit path to see if that works
		cmd := scpCommand(
			"-P", port,
			testFile,
			"test@localhost:/workspace/uploaded.txt")

		var stderr bytes.Buffer
		cmd.Stderr = &stderr

		err := cmd.Run()
		output := stderr.String()

		// This should also fail in the same way if the bug affects all paths
		if err != nil {
			t.Logf("SCP to explicit path also failed: %v", err)
			t.Logf("Output: %s", output)
			
			if strings.Contains(output, "close remote: Failure") {
				t.Log("Got same 'close remote: Failure' for explicit path")
			}
		}
	})

	t.Run("SCPToSlash", func(t *testing.T) {
		// Test what happens when we explicitly use /
		cmd := scpCommand(
			"-P", port,
			testFile,
			"test@localhost:/")

		var stderr bytes.Buffer
		cmd.Stderr = &stderr

		err := cmd.Run()
		output := stderr.String()

		if err != nil {
			t.Logf("SCP to / failed: %v", err)
			t.Logf("Output: %s", output)
		}
	})
}

func setupUbuntuContainer(t *testing.T) (string, func()) {
	// Use Ubuntu to match production environment
	cmd := exec.Command("docker", "run", "-d", "--rm",
		"ubuntu:22.04",
		"sh", "-c", "mkdir -p /workspace && chmod 755 /workspace && sleep 3600")

	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("Failed to create container: %v", err)
	}

	containerID := strings.TrimSpace(string(output))

	// Wait for container to be ready
	time.Sleep(1 * time.Second)

	cleanup := func() {
		exec.Command("docker", "kill", containerID).Run()
	}

	// Verify /workspace exists
	var stdout bytes.Buffer
	checkCmd := exec.Command("docker", "exec", containerID, "ls", "-la", "/workspace")
	checkCmd.Stdout = &stdout
	if err := checkCmd.Run(); err != nil {
		t.Logf("Warning: couldn't verify /workspace: %v", err)
	} else {
		t.Logf("/workspace contents: %s", stdout.String())
	}

	return containerID, cleanup
}

func handleProductionLikeSSHConnection(t *testing.T, conn net.Conn, config *ssh.ServerConfig, manager *DockerContainerManager, containerID string) {
	defer conn.Close()

	// Perform SSH handshake
	sshConn, channels, requests, err := ssh.NewServerConn(conn, config)
	if err != nil {
		t.Logf("SSH handshake failed: %v", err)
		return
	}
	defer sshConn.Close()

	// Discard global requests
	go ssh.DiscardRequests(requests)

	// Handle channels
	for newChannel := range channels {
		if newChannel.ChannelType() != "session" {
			newChannel.Reject(ssh.UnknownChannelType, "unknown channel type")
			continue
		}

		channel, requests, err := newChannel.Accept()
		if err != nil {
			continue
		}

		go handleProductionLikeChannel(t, channel, requests, manager, containerID)
	}
}

func handleProductionLikeChannel(t *testing.T, channel ssh.Channel, requests <-chan *ssh.Request, manager *DockerContainerManager, containerID string) {
	defer channel.Close()

	for req := range requests {
		switch req.Type {
		case "subsystem":
			if len(req.Payload) > 4 && string(req.Payload[4:]) == "sftp" {
				req.Reply(true, nil)

				// Create filesystem and handler exactly like production
				ctx := context.Background()
				fs := NewUnixContainerFS(manager, "test", containerID, "/workspace")
				// Use the fixed handler since the bug is fixed
				handler := NewSFTPHandler(ctx, fs, "/workspace")

				// Set up SFTP server with production configuration
				handlers := sftp.Handlers{
					FileGet:  handler,
					FilePut:  handler,
					FileCmd:  handler,
					FileList: handler,
				}

				server := sftp.NewRequestServer(channel, handlers)
				
				// Log when SFTP server starts and stops
				t.Logf("Starting SFTP server for container %s", containerID)
				err := server.Serve()
				if err != nil {
					if err != io.EOF {
						t.Logf("SFTP server error: %v", err)
					} else {
						t.Logf("SFTP server ended with EOF (normal)")
					}
				}
				t.Logf("SFTP server stopped")
				
				// Send exit status before closing
				exitStatus := []byte{0, 0, 0, 0} // exit status 0
				channel.SendRequest("exit-status", false, exitStatus)
				
				return
			}
			req.Reply(false, nil)
		default:
			if req.WantReply {
				req.Reply(false, nil)
			}
		}
	}
}

// TestSCPTildeWorkaround tests potential workarounds for the bug
func TestSCPTildeWorkaround(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	// Check if Docker is available
	if err := exec.Command("docker", "version").Run(); err != nil {
		t.Skip("Docker not available")
	}

	// Create and start Ubuntu container
	containerID, cleanup := setupUbuntuContainer(t)
	defer cleanup()

	// Start SSH server
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	hostKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	hostSigner, _ := ssh.NewSignerFromKey(hostKey)
	config := &ssh.ServerConfig{NoClientAuth: true}
	config.AddHostKey(hostSigner)

	manager := &DockerContainerManager{containerID: containerID}

	// Accept connections with a modified handler that might work around the bug
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}

			go func(conn net.Conn) {
				defer conn.Close()

				sshConn, channels, requests, err := ssh.NewServerConn(conn, config)
				if err != nil {
					return
				}
				defer sshConn.Close()

				go ssh.DiscardRequests(requests)

				for newChannel := range channels {
					if newChannel.ChannelType() != "session" {
						newChannel.Reject(ssh.UnknownChannelType, "unknown channel type")
						continue
					}

					channel, requests, err := newChannel.Accept()
					if err != nil {
						continue
					}

					go func(channel ssh.Channel, requests <-chan *ssh.Request) {
						defer channel.Close()

						for req := range requests {
							if req.Type == "subsystem" && string(req.Payload[4:]) == "sftp" {
								req.Reply(true, nil)

								ctx := context.Background()
								fs := NewUnixContainerFS(manager, "test", containerID, "/workspace")
								
								// Try using the current handler with the fix
								handler := NewSFTPHandler(ctx, fs, "/workspace")

								handlers := sftp.Handlers{
									FileGet:  handler,
									FilePut:  handler,
									FileCmd:  handler,
									FileList: handler,
								}

								server := sftp.NewRequestServer(channel, handlers)
								server.Serve()
								return
							}
							req.Reply(false, nil)
						}
					}(channel, requests)
				}
			}(conn)
		}
	}()

	time.Sleep(100 * time.Millisecond)

	// Create test file
	testFile := filepath.Join(t.TempDir(), "test.txt")
	os.WriteFile(testFile, []byte("test"), 0644)

	addr := listener.Addr().String()
	port := strings.Split(addr, ":")[len(strings.Split(addr, ":"))-1]

	// Test with the current "fixed" handler
	cmd := scpCommand("-P", port, testFile, "test@localhost:~")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	err = cmd.Run()
	if err != nil {
		t.Logf("With current handler: still fails with: %v", err)
		t.Logf("Output: %s", stderr.String())
		
		if strings.Contains(stderr.String(), "close remote: Failure") {
			t.Log("Confirmed: Current fix does NOT resolve the issue")
		}
	} else {
		t.Log("With current handler: upload succeeded")
		
		// Verify file was created
		var stdout bytes.Buffer
		checkCmd := exec.Command("docker", "exec", containerID, "ls", "-la", "/workspace")
		checkCmd.Stdout = &stdout
		checkCmd.Run()
		t.Logf("Workspace contents: %s", stdout.String())
	}
}

// TestDebugSFTPProtocol helps us understand what SCP is actually sending
func TestDebugSFTPProtocol(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping debug test")
	}

	if err := exec.Command("docker", "version").Run(); err != nil {
		t.Skip("Docker not available")
	}

	containerID, cleanup := setupUbuntuContainer(t)
	defer cleanup()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	hostKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	hostSigner, _ := ssh.NewSignerFromKey(hostKey)
	config := &ssh.ServerConfig{NoClientAuth: true}
	config.AddHostKey(hostSigner)

	manager := &DockerContainerManager{containerID: containerID}

	// Create a channel to capture what paths SCP sends
	pathsChan := make(chan string, 10)

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}

			go func(conn net.Conn) {
				defer conn.Close()

				sshConn, channels, requests, err := ssh.NewServerConn(conn, config)
				if err != nil {
					return
				}
				defer sshConn.Close()

				go ssh.DiscardRequests(requests)

				for newChannel := range channels {
					if newChannel.ChannelType() != "session" {
						newChannel.Reject(ssh.UnknownChannelType, "unknown channel type")
						continue
					}

					channel, requests, err := newChannel.Accept()
					if err != nil {
						continue
					}

					go func(channel ssh.Channel, requests <-chan *ssh.Request) {
						defer channel.Close()

						for req := range requests {
							if req.Type == "subsystem" && string(req.Payload[4:]) == "sftp" {
								req.Reply(true, nil)

								ctx := context.Background()
								fs := NewUnixContainerFS(manager, "test", containerID, "/workspace")
								
								// Create a debug handler that logs paths
								handler := &debugSFTPHandler{
									SFTPHandler: NewSFTPHandler(ctx, fs, "/workspace"),
									pathsChan:   pathsChan,
								}

								handlers := sftp.Handlers{
									FileGet:  handler,
									FilePut:  handler,
									FileCmd:  handler,
									FileList: handler,
								}

								server := sftp.NewRequestServer(channel, handlers)
								server.Serve()
								return
							}
							req.Reply(false, nil)
						}
					}(channel, requests)
				}
			}(conn)
		}
	}()

	time.Sleep(100 * time.Millisecond)

	testFile := filepath.Join(t.TempDir(), "test.txt")
	os.WriteFile(testFile, []byte("test"), 0644)

	addr := listener.Addr().String()
	port := strings.Split(addr, ":")[len(strings.Split(addr, ":"))-1]

	// Test what path SCP sends for ~
	t.Run("PathForTilde", func(t *testing.T) {
		cmd := scpCommand("-P", port, testFile, "test@localhost:~")
		cmd.Run()

		select {
		case path := <-pathsChan:
			t.Logf("When user types '~', SCP sends path: %q", path)
		case <-time.After(1 * time.Second):
			t.Log("No path received")
		}
	})

	// Test what path SCP sends for explicit /workspace
	t.Run("PathForExplicit", func(t *testing.T) {
		cmd := scpCommand("-P", port, testFile, "test@localhost:/workspace/file.txt")
		cmd.Run()

		select {
		case path := <-pathsChan:
			t.Logf("When user types '/workspace/file.txt', SCP sends path: %q", path)
		case <-time.After(1 * time.Second):
			t.Log("No path received")
		}
	})
}

// debugSFTPHandler wraps SFTPHandler to log what paths are being requested
type debugSFTPHandler struct {
	*SFTPHandler
	pathsChan chan string
}

func (h *debugSFTPHandler) Filewrite(req *sftp.Request) (io.WriterAt, error) {
	// Send the path to our channel for debugging
	select {
	case h.pathsChan <- req.Filepath:
	default:
	}
	
	// Log to test output
	fmt.Fprintf(os.Stderr, "DEBUG: Filewrite called with path: %q\n", req.Filepath)
	
	return h.SFTPHandler.Filewrite(req)
}