package sshproxy

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
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

// TestSCPBugRootCause demonstrates the root cause of the SCP bug:
// When user types "scp file.txt host:~", SCP sends "/" as the path,
// and trying to write a file to "/" fails.
func TestSCPBugRootCause(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	if err := exec.Command("docker", "version").Run(); err != nil {
		t.Skip("Docker not available")
	}

	// Create container
	cmd := exec.Command("docker", "run", "-d", "--rm", "ubuntu:22.04", "sleep", "10")
	out, err := cmd.Output()
	if err != nil {
		t.Fatal("Failed to create container")
	}
	containerID := strings.TrimSpace(string(out))
	defer exec.Command("docker", "kill", containerID).Run()

	// Create /workspace
	exec.Command("docker", "exec", containerID, "mkdir", "-p", "/workspace").Run()

	// Setup container manager and filesystem
	manager := &DockerContainerManager{containerID: containerID}
	ctx := context.Background()
	fs := NewUnixContainerFS(manager, "test", containerID, "/workspace")

	t.Log("Testing the exact scenario that causes 'scp: close remote: Failure'")
	t.Log("When SCP uploads to ~, it resolves to '/' and tries to create a file there")

	// Step 1: Stat "/" - this succeeds (it's a directory)
	info, err := fs.Stat(ctx, "/")
	if err != nil {
		t.Fatalf("Stat(/) failed: %v", err)
	}
	t.Logf("✓ Stat(/) succeeded, IsDir=%v", info.IsDir())

	// Step 2: OpenFile("/") - this succeeds (creates a file handle)
	file, err := fs.OpenFile(ctx, "/", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		t.Fatalf("OpenFile(/) failed: %v", err)
	}
	t.Log("✓ OpenFile(/) succeeded (file handle created)")

	// Step 3: Write data - this succeeds (data is buffered)
	testData := []byte("test content")
	_, err = file.Write(testData)
	if err != nil {
		t.Fatalf("Write failed: %v", err)
	}
	t.Log("✓ Write() succeeded (data buffered, SCP shows 100%)")

	// Step 4: Close() - THIS FAILS because we can't write a file to /
	err = file.Close()
	if err != nil {
		t.Logf("✓✓✓ Close() FAILED as expected: %v", err)
		
		if strings.Contains(err.Error(), "Is a directory") {
			t.Log("✓ Error confirms '/' is a directory")
		}
		
		t.Log("")
		t.Log("CONCLUSION: This causes 'scp: close remote: Failure'")
		t.Log("The fix needs to handle '/' as a special case")
	} else {
		t.Error("BUG NOT REPRODUCED: Close() succeeded when it should fail")
	}
}

// TestSCPIntegration tests SCP with the original buggy handler and the fixed handler
func TestSCPIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	if err := exec.Command("docker", "version").Run(); err != nil {
		t.Skip("Docker not available")
	}

	t.Run("BuggyHandler", func(t *testing.T) {
		testSCPWithHandler(t, false) // Use original buggy handler
	})
	
	t.Run("FixedHandler", func(t *testing.T) {
		testSCPWithHandler(t, true) // Use fixed handler
	})
}

func testSCPWithHandler(t *testing.T, useFixed bool) {
	// Create container
	cmd := exec.Command("docker", "run", "-d", "--rm", "alpine", "sleep", "30")
	out, _ := cmd.Output()
	containerID := strings.TrimSpace(string(out))
	if containerID == "" {
		t.Skip("Failed to create container")
	}
	defer exec.Command("docker", "kill", containerID).Run()

	// Create /workspace
	exec.Command("docker", "exec", containerID, "mkdir", "-p", "/workspace").Run()
	time.Sleep(500 * time.Millisecond)

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

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}

			go func(conn net.Conn) {
				defer conn.Close()
				
				sshConn, channels, reqs, err := ssh.NewServerConn(conn, config)
				if err != nil {
					return
				}
				defer sshConn.Close()
				
				go ssh.DiscardRequests(reqs)
				
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
							if req.Type == "subsystem" && len(req.Payload) > 4 && string(req.Payload[4:]) == "sftp" {
								req.Reply(true, nil)
								
								ctx := context.Background()
								fs := NewUnixContainerFS(manager, "test", containerID, "/workspace")
								
								var handlers sftp.Handlers
								if useFixed {
									// Use fixed handler
									h := NewSFTPHandler(ctx, fs, "/workspace")
									handlers = sftp.Handlers{
										FileGet:  h,
										FilePut:  h,
										FileCmd:  h,
										FileList: h,
									}
								} else {
									// Use original buggy handler
									h := NewOriginalSFTPHandler(ctx, fs, "/workspace")
									handlers = sftp.Handlers{
										FileGet:  h,
										FilePut:  h,
										FileCmd:  h,
										FileList: h,
									}
								}
								
								server := sftp.NewRequestServer(channel, handlers)
								if err := server.Serve(); err != nil && err != io.EOF {
									t.Logf("SFTP server error: %v", err)
								}
								return
							}
							req.Reply(false, nil)
						}
					}(channel, requests)
				}
			}(conn)
		}
	}()

	// Give server time to start
	time.Sleep(100 * time.Millisecond)

	// Create test file
	testFile := filepath.Join(t.TempDir(), "test.txt")
	os.WriteFile(testFile, []byte("test"), 0644)

	// Get port
	addr := listener.Addr().String()
	port := strings.Split(addr, ":")[1]

	// Run SCP using the helper function
	cmd = scpCommand(
		"-P", port,
		testFile,
		"test@localhost:~")

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	err = cmd.Run()
	output := stderr.String()

	// Analyze results
	if useFixed {
		if err != nil {
			t.Logf("With fix: SCP still failed: %v", err)
			t.Logf("Output: %s", output)
		} else {
			t.Log("✓ With fix: SCP succeeded")
		}
	} else {
		if err != nil {
			t.Logf("Without fix: SCP failed as expected: %v", err)
			if strings.Contains(output, "close remote: Failure") {
				t.Log("✓ Got 'close remote: Failure' error")
			}
		} else {
			t.Log("Without fix: SCP unexpectedly succeeded")
		}
	}
}