package sshproxy

import (
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

// TestSCPRealBug demonstrates the actual bug with minimal setup
func TestSCPRealBug(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	if err := exec.Command("docker", "version").Run(); err != nil {
		t.Skip("Docker not available")
	}

	// Test both the original and "fixed" handlers
	testCases := []struct {
		name        string
		useOriginal bool
	}{
		{"OriginalHandler", true},
		{"CurrentHandler", false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Create Ubuntu container
			containerID := createSimpleContainer(t)
			defer exec.Command("docker", "kill", containerID).Run()

			// Start SSH server
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()

			hostKey, _ := rsa.GenerateKey(rand.Reader, 2048)
			hostSigner, _ := ssh.NewSignerFromKey(hostKey)
			
			config := &ssh.ServerConfig{
				NoClientAuth: true,
			}
			config.AddHostKey(hostSigner)

			manager := &DockerContainerManager{containerID: containerID}
			
			// Track errors from the SFTP server
			sftpErrors := make(chan error, 10)

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
										
										var handler sftp.Handlers
										if tc.useOriginal {
											h := NewOriginalSFTPHandler(ctx, fs, "/workspace")
											handler = sftp.Handlers{
												FileGet:  h,
												FilePut:  h,
												FileCmd:  h,
												FileList: h,
											}
										} else {
											h := NewSFTPHandler(ctx, fs, "/workspace")
											handler = sftp.Handlers{
												FileGet:  h,
												FilePut:  h,
												FileCmd:  h,
												FileList: h,
											}
										}

										server := sftp.NewRequestServer(channel, handler)
										if err := server.Serve(); err != nil && err != io.EOF {
											select {
											case sftpErrors <- err:
											default:
											}
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

			time.Sleep(100 * time.Millisecond)

			// Create test file
			testFile := filepath.Join(t.TempDir(), "test.txt")
			testContent := []byte("test content")
			os.WriteFile(testFile, testContent, 0644)

			port := strings.Split(listener.Addr().String(), ":")[1]

			// Test 1: SCP to ~ (which becomes /)
			t.Run("ToTilde", func(t *testing.T) {
				cmd := scpCommand("-P", port, testFile, "test@localhost:~")
				
				output, err := cmd.CombinedOutput()
				
				if tc.useOriginal {
					// Original handler should fail
					if err == nil {
						t.Error("Expected failure with original handler, but succeeded")
					} else if strings.Contains(string(output), "close remote: Failure") {
						t.Log("✓ Got expected 'close remote: Failure' with original handler")
					} else {
						t.Logf("Got different error: %v\nOutput: %s", err, output)
					}
				} else {
					// Current handler might work or fail differently
					if err == nil {
						t.Log("✓ Current handler succeeded")
						// Verify file was created
						checkCmd := exec.Command("docker", "exec", containerID, "ls", "/workspace")
						if out, err := checkCmd.Output(); err == nil {
							t.Logf("Files in /workspace: %s", out)
						}
					} else if strings.Contains(string(output), "close remote: Failure") {
						t.Log("Current handler still has the bug: 'close remote: Failure'")
					} else {
						t.Logf("Current handler failed differently: %v\nOutput: %s", err, output)
					}
				}

				// Check for SFTP server errors
				select {
				case sftpErr := <-sftpErrors:
					t.Logf("SFTP server error: %v", sftpErr)
				default:
				}
			})

			// Test 2: SCP to explicit path
			t.Run("ToExplicitPath", func(t *testing.T) {
				cmd := scpCommand("-P", port, testFile, "test@localhost:/workspace/uploaded.txt")
				
				output, err := cmd.CombinedOutput()
				
				if err != nil {
					if strings.Contains(string(output), "close remote: Failure") {
						t.Logf("Got 'close remote: Failure' for explicit path")
					} else {
						t.Logf("Failed with: %v\nOutput: %s", err, output)
					}
				} else {
					t.Log("Succeeded uploading to explicit path")
				}
			})
		})
	}
}

func createSimpleContainer(t *testing.T) string {
	cmd := exec.Command("docker", "run", "-d", "--rm", "ubuntu:22.04", 
		"sh", "-c", "mkdir -p /workspace && sleep 300")
	
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("Failed to create container: %v", err)
	}
	
	containerID := strings.TrimSpace(string(output))
	time.Sleep(500 * time.Millisecond) // Let container start
	
	return containerID
}

// TestDirectWriteToRoot tests writing directly to / in a container
func TestDirectWriteToRoot(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	if err := exec.Command("docker", "version").Run(); err != nil {
		t.Skip("Docker not available")
	}

	containerID := createSimpleContainer(t)
	defer exec.Command("docker", "kill", containerID).Run()

	manager := &DockerContainerManager{containerID: containerID}
	ctx := context.Background()
	fs := NewUnixContainerFS(manager, "test", containerID, "/workspace")

	// Test what happens when we try to write to "/"
	t.Run("WriteToRoot", func(t *testing.T) {
		file, err := fs.OpenFile(ctx, "/", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
		if err != nil {
			t.Logf("OpenFile(/) failed immediately: %v", err)
			return
		}

		// Write some data
		n, err := file.Write([]byte("test"))
		if err != nil {
			t.Logf("Write failed: %v", err)
		} else {
			t.Logf("Write succeeded, wrote %d bytes", n)
		}

		// The critical moment - Close()
		err = file.Close()
		if err != nil {
			t.Logf("✓ Close() failed as expected: %v", err)
			if strings.Contains(err.Error(), "Is a directory") {
				t.Log("✓ Error confirms / is a directory")
			}
		} else {
			t.Error("Close() succeeded when it should have failed")
		}
	})

	// Test what the original handler's resolvePath does with "/"
	t.Run("ResolvePathBehavior", func(t *testing.T) {
		originalHandler := NewOriginalSFTPHandler(ctx, fs, "/workspace")
		currentHandler := NewSFTPHandler(ctx, fs, "/workspace")

		// Test how each handler resolves "/"
		originalResolved := originalHandler.resolvePath("/")
		currentResolved := currentHandler.resolvePath("/")

		t.Logf("Original handler resolves '/' to: %q", originalResolved)
		t.Logf("Current handler resolves '/' to: %q", currentResolved)

		if originalResolved != currentResolved {
			t.Log("✓ Handlers resolve '/' differently")
		}
	})
}