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

// TestSCPFixVerification verifies that the fix for the SCP ~ bug works correctly
func TestSCPFixVerification(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	if err := exec.Command("docker", "version").Run(); err != nil {
		t.Skip("Docker not available")
	}

	// Create container
	cmd := exec.Command("docker", "run", "-d", "--rm", "ubuntu:22.04",
		"sh", "-c", "mkdir -p /workspace && chmod 755 /workspace && sleep 300")
	output, err := cmd.Output()
	if err != nil {
		t.Fatal("Failed to create container")
	}
	containerID := strings.TrimSpace(string(output))
	defer exec.Command("docker", "kill", containerID).Run()
	time.Sleep(500 * time.Millisecond)

	// Set up SSH server with fixed handler
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
							if req.Type == "subsystem" && len(req.Payload) > 4 && string(req.Payload[4:]) == "sftp" {
								req.Reply(true, nil)

								ctx := context.Background()
								fs := NewUnixContainerFS(manager, "test", containerID, "/workspace")
								handler := NewSFTPHandler(ctx, fs, "/workspace") // Use FIXED handler

								handlers := sftp.Handlers{
									FileGet:  handler,
									FilePut:  handler,
									FileCmd:  handler,
									FileList: handler,
								}

								server := sftp.NewRequestServer(channel, handlers)
								err := server.Serve()
								if err != nil {
									if err != io.EOF {
										t.Logf("SFTP server error: %v", err)
									}
								}

								// Send exit status before closing
								exitStatus := []byte{0, 0, 0, 0} // exit status 0
								channel.SendRequest("exit-status", false, exitStatus)

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
	testFile := filepath.Join(t.TempDir(), "junk.txt")
	testContent := []byte("Test content that should upload successfully")
	os.WriteFile(testFile, testContent, 0644)

	port := strings.Split(listener.Addr().String(), ":")[1]

	// Test SCP to ~
	t.Run("SCPToTilde", func(t *testing.T) {
		cmd := scpCommand("-P", port, testFile, "user@localhost:~")
		output, err := cmd.CombinedOutput()

		if err != nil {
			t.Errorf("SCP failed: %v", err)
			t.Logf("Output: %s", output)
			if strings.Contains(string(output), "close remote: Failure") {
				t.Error("Still getting 'close remote: Failure' - fix not working!")
			}
		} else {
			t.Log("✓ SCP to ~ succeeded with the fix!")

			// Verify file was created in /workspace
			var out strings.Builder
			checkCmd := exec.Command("docker", "exec", containerID, "ls", "-la", "/workspace")
			checkCmd.Stdout = &out
			checkCmd.Run()

			filesInWorkspace := out.String()
			if strings.Contains(filesInWorkspace, "junk.txt") {
				t.Log("✓ File successfully created in /workspace")

				// Check content
				out.Reset()
				checkCmd = exec.Command("docker", "exec", containerID, "cat", "/workspace/junk.txt")
				checkCmd.Stdout = &out
				checkCmd.Run()

				if strings.TrimSpace(out.String()) == string(testContent) {
					t.Log("✓ File content matches")
				}
			} else {
				t.Errorf("File not found in /workspace: %s", filesInWorkspace)
			}
		}
	})

	// Test other paths to ensure we didn't break them
	t.Run("SCPToExplicitPath", func(t *testing.T) {
		cmd := scpCommand("-P", port, testFile, "user@localhost:/workspace/explicit.txt")
		output, err := cmd.CombinedOutput()

		if err != nil {
			t.Errorf("SCP to explicit path failed: %v", err)
			t.Logf("Output: %s", output)
		} else {
			t.Log("✓ SCP to explicit path still works")
		}
	})

	t.Run("SCPToRelativePath", func(t *testing.T) {
		cmd := scpCommand("-P", port, testFile, "user@localhost:relative.txt")
		output, err := cmd.CombinedOutput()

		if err != nil {
			t.Errorf("SCP to relative path failed: %v", err)
			t.Logf("Output: %s", output)
		} else {
			t.Log("✓ SCP to relative path still works")

			// Should be in /workspace
			var out strings.Builder
			checkCmd := exec.Command("docker", "exec", containerID, "ls", "/workspace/relative.txt")
			checkCmd.Stdout = &out
			err := checkCmd.Run()
			if err == nil {
				t.Log("✓ Relative path correctly resolved to /workspace")
			}
		}
	})
}

// TestPathResolutionWithFix verifies the path resolution logic with the fix
func TestPathResolutionWithFix(t *testing.T) {
	ctx := context.Background()
	handler := &SFTPHandler{
		homeDir: "/workspace",
		ctx:     ctx,
	}

	testCases := []struct {
		input    string
		expected string
		comment  string
	}{
		{"/", "/workspace", "Root should map to home"},
		{"/test.txt", "/workspace/test.txt", "Root-level files should map to home (THE FIX)"},
		{"/dir/file.txt", "/workspace/dir/file.txt", "Root-level paths should map to home"},
		{"~", "/workspace", "Tilde should map to home"},
		{"~/test.txt", "/workspace/test.txt", "Tilde paths should work"},
		{"test.txt", "/workspace/test.txt", "Relative paths should be relative to home"},
		{"./test.txt", "/workspace/test.txt", "Dot-relative paths should work"},
		{"/workspace/test.txt", "/workspace/test.txt", "Paths already in workspace should stay"},
		{"/workspace/dir/file.txt", "/workspace/dir/file.txt", "Nested workspace paths should stay"},
	}

	for _, tc := range testCases {
		t.Run(tc.input, func(t *testing.T) {
			result := handler.resolvePath(tc.input)
			if result != tc.expected {
				t.Errorf("resolvePath(%q) = %q, want %q (%s)",
					tc.input, result, tc.expected, tc.comment)
			} else {
				t.Logf("✓ %q → %q (%s)", tc.input, result, tc.comment)
			}
		})
	}
}
