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

// TestSCPProductionBug demonstrates the exact production bug with SCP to ~
// This test shows that:
// 1. When user types "scp file host:~", SCP sends "/" as the path
// 2. The original handler tries to write to "/" which fails
// 3. The error manifests as "close remote: Failure" in some cases
func TestSCPProductionBug(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	if err := exec.Command("docker", "version").Run(); err != nil {
		t.Skip("Docker not available")
	}

	// Create Ubuntu container matching production
	cmd := exec.Command("docker", "run", "-d", "--rm", "ubuntu:22.04",
		"sh", "-c", "mkdir -p /workspace && chmod 755 /workspace && sleep 300")
	output, _ := cmd.Output()
	containerID := strings.TrimSpace(string(output))
	defer exec.Command("docker", "kill", containerID).Run()
	time.Sleep(500 * time.Millisecond)

	// Set up SSH server
	listener, _ := net.Listen("tcp", "127.0.0.1:0")
	defer listener.Close()

	hostKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	hostSigner, _ := ssh.NewSignerFromKey(hostKey)
	config := &ssh.ServerConfig{NoClientAuth: true}
	config.AddHostKey(hostSigner)

	manager := &DockerContainerManager{containerID: containerID}

	// Track what happens in SFTP
	eventChan := make(chan string, 100)

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}

			go func(conn net.Conn) {
				defer conn.Close()

				sshConn, channels, requests, _ := ssh.NewServerConn(conn, config)
				if sshConn == nil {
					return
				}
				defer sshConn.Close()

				go ssh.DiscardRequests(requests)

				for newChannel := range channels {
					if newChannel.ChannelType() != "session" {
						newChannel.Reject(ssh.UnknownChannelType, "unknown channel type")
						continue
					}

					channel, requests, _ := newChannel.Accept()
					if channel == nil {
						continue
					}

					go func(channel ssh.Channel, requests <-chan *ssh.Request) {
						defer channel.Close()

						for req := range requests {
							if req.Type == "subsystem" && string(req.Payload[4:]) == "sftp" {
								req.Reply(true, nil)

								ctx := context.Background()
								fs := NewUnixContainerFS(manager, "test", containerID, "/workspace")

								// Use a wrapper that logs events
								originalHandler := NewOriginalSFTPHandler(ctx, fs, "/workspace")
								handler := &loggingHandler{
									handler:   originalHandler,
									eventChan: eventChan,
								}

								handlers := sftp.Handlers{
									FileGet:  handler,
									FilePut:  handler,
									FileCmd:  handler,
									FileList: handler,
								}

								server := sftp.NewRequestServer(channel, handlers)
								err := server.Serve()
								if err != nil && err != io.EOF {
									select {
									case eventChan <- "SFTP_ERROR: " + err.Error():
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
	os.WriteFile(testFile, []byte("test content"), 0644)

	port := strings.Split(listener.Addr().String(), ":")[1]

	// The actual test
	t.Run("BugDemonstration", func(t *testing.T) {
		// Clear events
		for len(eventChan) > 0 {
			<-eventChan
		}

		// Run the exact command that fails in production
		cmd := scpCommand("-P", port, testFile, "user@localhost:~")
		output, err := cmd.CombinedOutput()

		// Collect events
		time.Sleep(100 * time.Millisecond)
		var events []string
		for len(eventChan) > 0 {
			events = append(events, <-eventChan)
		}

		// Report findings
		t.Log("=== BUG DEMONSTRATION ===")
		t.Logf("Command: scp test.txt user@localhost:~")
		t.Logf("Exit code: %v", err)
		t.Logf("Output: %s", string(output))

		t.Log("\n=== SFTP Events ===")
		for _, event := range events {
			t.Log(event)
		}

		// Check for the bug signature
		if err != nil {
			if strings.Contains(string(output), "close remote: Failure") {
				t.Log("\n✓✓✓ BUG CONFIRMED: Got 'close remote: Failure' error")
				t.Log("This happens because:")
				t.Log("1. User types 'scp file host:~'")
				t.Log("2. SCP resolves ~ and sends '/' as the path")
				t.Log("3. Handler tries to create a file at '/'")
				t.Log("4. Write is buffered and appears to succeed (100%)")
				t.Log("5. On Close(), writing to '/' fails: 'Is a directory'")
				t.Log("6. SCP reports this as 'close remote: Failure'")
			} else {
				t.Log("\nBug manifested differently in this environment")
			}
		}
	})

	// Also test the fix
	t.Run("WithFix", func(t *testing.T) {
		// Create a new server with the fixed handler
		listener2, _ := net.Listen("tcp", "127.0.0.1:0")
		defer listener2.Close()

		go func() {
			for {
				conn, err := listener2.Accept()
				if err != nil {
					return
				}

				go func(conn net.Conn) {
					defer conn.Close()

					sshConn, channels, requests, _ := ssh.NewServerConn(conn, config)
					if sshConn == nil {
						return
					}
					defer sshConn.Close()

					go ssh.DiscardRequests(requests)

					for newChannel := range channels {
						if newChannel.ChannelType() != "session" {
							newChannel.Reject(ssh.UnknownChannelType, "unknown channel type")
							continue
						}

						channel, requests, _ := newChannel.Accept()
						if channel == nil {
							continue
						}

						go func(channel ssh.Channel, requests <-chan *ssh.Request) {
							defer channel.Close()

							for req := range requests {
								if req.Type == "subsystem" && string(req.Payload[4:]) == "sftp" {
									req.Reply(true, nil)

									ctx := context.Background()
									fs := NewUnixContainerFS(manager, "test", containerID, "/workspace")
									handler := NewSFTPHandler(ctx, fs, "/workspace") // Use fixed handler

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
		port2 := strings.Split(listener2.Addr().String(), ":")[1]

		cmd := scpCommand("-P", port2, testFile, "user@localhost:~")
		output, err := cmd.CombinedOutput()

		if err == nil {
			t.Log("\n✓ With fix: Upload succeeded")
			// Check if file was created
			checkCmd := exec.Command("docker", "exec", containerID, "ls", "-la", "/workspace")
			if out, _ := checkCmd.Output(); out != nil {
				t.Logf("Files in /workspace: %s", out)
			}
		} else {
			t.Logf("\nWith fix: Still failed with: %v", err)
			t.Logf("Output: %s", output)
		}
	})
}

// loggingHandler wraps an SFTP handler to log events
type loggingHandler struct {
	handler   *OriginalSFTPHandler
	eventChan chan string
}

func (h *loggingHandler) Fileread(req *sftp.Request) (io.ReaderAt, error) {
	select {
	case h.eventChan <- "Fileread: " + req.Filepath:
	default:
	}
	return h.handler.Fileread(req)
}

func (h *loggingHandler) Filewrite(req *sftp.Request) (io.WriterAt, error) {
	select {
	case h.eventChan <- "Filewrite: " + req.Filepath:
	default:
	}
	return h.handler.Filewrite(req)
}

func (h *loggingHandler) Filecmd(req *sftp.Request) error {
	select {
	case h.eventChan <- "Filecmd: " + req.Method + " " + req.Filepath:
	default:
	}
	return h.handler.Filecmd(req)
}

func (h *loggingHandler) Filelist(req *sftp.Request) (sftp.ListerAt, error) {
	select {
	case h.eventChan <- "Filelist: " + req.Filepath:
	default:
	}
	return h.handler.Filelist(req)
}
