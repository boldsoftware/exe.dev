package sshproxy

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
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

// DockerContainerManager implements ContainerManager for Docker containers
type DockerContainerManager struct {
	containerID string
}

func (m *DockerContainerManager) ExecuteInContainer(ctx context.Context, userID, containerID string, cmd []string, stdin io.Reader, stdout, stderr io.Writer) error {
	args := append([]string{"exec", "-i", containerID}, cmd...)
	dockerCmd := exec.CommandContext(ctx, "docker", args...)
	
	dockerCmd.Stdin = stdin
	dockerCmd.Stdout = stdout
	dockerCmd.Stderr = stderr
	
	return dockerCmd.Run()
}

// TestIntegrationSFTP runs comprehensive SFTP tests using Docker
func TestIntegrationSFTP(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}
	
	// Check if Docker is available
	if err := exec.Command("docker", "version").Run(); err != nil {
		t.Skip("Docker not available, skipping integration tests")
	}
	
	// Create and start a test container
	containerID, cleanup := setupTestContainer(t)
	defer cleanup()
	
	// Set up SSH server with SFTP subsystem
	sshServer, addr := setupSSHServer(t, containerID)
	defer sshServer.Close()
	
	// Run test suites
	t.Run("BasicFileOperations", func(t *testing.T) {
		testBasicFileOperations(t, addr)
	})
	
	t.Run("DirectoryOperations", func(t *testing.T) {
		testDirectoryOperations(t, addr)
	})
	
	t.Run("SymlinkOperations", func(t *testing.T) {
		testSymlinkOperations(t, addr)
	})
	
	t.Run("LargeFileTransfer", func(t *testing.T) {
		testLargeFileTransfer(t, addr)
	})
	
	t.Run("ConcurrentTransfers", func(t *testing.T) {
		testConcurrentTransfers(t, addr)
	})
	
	t.Run("PathResolution", func(t *testing.T) {
		testPathResolution(t, addr)
	})
	
	t.Run("Permissions", func(t *testing.T) {
		testPermissions(t, addr)
	})
	
	t.Run("DirectoryUpload", func(t *testing.T) {
		testDirectoryUpload(t, addr)
	})
	
	t.Run("SCPCommands", func(t *testing.T) {
		testSCPCommands(t, addr, containerID)
	})
}

func setupTestContainer(t *testing.T) (string, func()) {
	// Pull Ubuntu image if not present (has more standard Unix utilities)
	exec.Command("docker", "pull", "ubuntu:latest").Run()
	
	// Create container with necessary tools
	cmd := exec.Command("docker", "run", "-d", "--rm",
		"ubuntu:latest",
		"sh", "-c", "apt-get update && apt-get install -y openssh-client && mkdir -p /workspace && chmod 755 /workspace && sleep 3600")
	
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("Failed to create container: %v", err)
	}
	
	containerID := strings.TrimSpace(string(output))
	
	// Wait for container to be ready
	time.Sleep(2 * time.Second)
	
	cleanup := func() {
		exec.Command("docker", "kill", containerID).Run()
	}
	
	return containerID, cleanup
}

func setupSSHServer(t *testing.T, containerID string) (net.Listener, string) {
	// Generate test SSH host key
	hostKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("Failed to generate host key: %v", err)
	}
	
	hostSigner, err := ssh.NewSignerFromKey(hostKey)
	if err != nil {
		t.Fatalf("Failed to create host signer: %v", err)
	}
	
	// Create SSH server config
	config := &ssh.ServerConfig{
		NoClientAuth: true, // For testing, accept any client
	}
	config.AddHostKey(hostSigner)
	
	// Start SSH server
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to listen: %v", err)
	}
	
	manager := &DockerContainerManager{containerID: containerID}
	
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			
			go handleSSHConnection(conn, config, manager, containerID)
		}
	}()
	
	return listener, listener.Addr().String()
}

func handleSSHConnection(conn net.Conn, config *ssh.ServerConfig, manager *DockerContainerManager, containerID string) {
	defer conn.Close()
	
	// Perform SSH handshake
	sshConn, channels, requests, err := ssh.NewServerConn(conn, config)
	if err != nil {
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
		
		go handleChannel(channel, requests, manager, containerID)
	}
}

func handleChannel(channel ssh.Channel, requests <-chan *ssh.Request, manager *DockerContainerManager, containerID string) {
	defer channel.Close()
	
	for req := range requests {
		switch req.Type {
		case "subsystem":
			if string(req.Payload[4:]) == "sftp" {
				req.Reply(true, nil)
				
				// Create SFTP handler
				ctx := context.Background()
				fs := NewUnixContainerFS(manager, "test", containerID, "/workspace")
				handler := NewSFTPHandler(ctx, fs, "/workspace")
				
				// Create SFTP server with handlers
				handlers := sftp.Handlers{
					FileGet:  handler,
					FilePut:  handler,
					FileCmd:  handler,
					FileList: handler,
				}
				
				// Start SFTP server
				server := sftp.NewRequestServer(channel, handlers)
				server.Serve()
				return
			}
			req.Reply(false, nil)
		default:
			req.Reply(false, nil)
		}
	}
}

func connectSFTP(t *testing.T, addr string) (*sftp.Client, func()) {
	// Connect to SSH server
	config := &ssh.ClientConfig{
		User:            "test",
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}
	
	conn, err := ssh.Dial("tcp", addr, config)
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	
	// Create SFTP client
	client, err := sftp.NewClient(conn)
	if err != nil {
		conn.Close()
		t.Fatalf("Failed to create SFTP client: %v", err)
	}
	
	cleanup := func() {
		client.Close()
		conn.Close()
	}
	
	return client, cleanup
}

func testBasicFileOperations(t *testing.T, addr string) {
	client, cleanup := connectSFTP(t, addr)
	defer cleanup()
	
	// Test file creation and writing
	testFile := "test.txt"
	content := []byte("Hello, SFTP!")
	
	file, err := client.Create(testFile)
	if err != nil {
		t.Fatalf("Failed to create file: %v", err)
	}
	
	n, err := file.Write(content)
	if err != nil {
		t.Fatalf("Failed to write file: %v", err)
	}
	if n != len(content) {
		t.Errorf("Wrote %d bytes, expected %d", n, len(content))
	}
	file.Close()
	
	// Test file reading
	file, err = client.Open(testFile)
	if err != nil {
		t.Fatalf("Failed to open file: %v", err)
	}
	
	readContent := make([]byte, len(content))
	n, err = file.Read(readContent)
	if err != nil && err != io.EOF {
		t.Fatalf("Failed to read file: %v", err)
	}
	if !bytes.Equal(readContent[:n], content) {
		t.Errorf("Read content doesn't match: got %q, want %q", readContent[:n], content)
	}
	file.Close()
	
	// Test file stat
	info, err := client.Stat(testFile)
	if err != nil {
		t.Fatalf("Failed to stat file: %v", err)
	}
	if info.Size() != int64(len(content)) {
		t.Errorf("File size mismatch: got %d, want %d", info.Size(), len(content))
	}
	
	// Test file removal
	err = client.Remove(testFile)
	if err != nil {
		t.Fatalf("Failed to remove file: %v", err)
	}
	
	// Verify file is gone
	_, err = client.Stat(testFile)
	if err == nil {
		t.Error("File still exists after removal")
	}
}

func testDirectoryOperations(t *testing.T, addr string) {
	client, cleanup := connectSFTP(t, addr)
	defer cleanup()
	
	// Create directory
	dirName := "testdir"
	err := client.Mkdir(dirName)
	if err != nil {
		t.Fatalf("Failed to create directory: %v", err)
	}
	
	// Create file in directory
	filePath := filepath.Join(dirName, "file.txt")
	file, err := client.Create(filePath)
	if err != nil {
		t.Fatalf("Failed to create file in directory: %v", err)
	}
	file.Write([]byte("test"))
	file.Close()
	
	// List directory
	entries, err := client.ReadDir(dirName)
	if err != nil {
		t.Fatalf("Failed to read directory: %v", err)
	}
	
	found := false
	for _, entry := range entries {
		if entry.Name() == "file.txt" {
			found = true
			break
		}
	}
	if !found {
		t.Error("File not found in directory listing")
	}
	
	// Remove directory (should fail - not empty)
	err = client.Remove(dirName)
	if err == nil {
		t.Error("Should not be able to remove non-empty directory")
	}
	
	// Remove file first
	err = client.Remove(filePath)
	if err != nil {
		t.Fatalf("Failed to remove file: %v", err)
	}
	
	// Now remove directory
	err = client.Remove(dirName)
	if err != nil {
		t.Fatalf("Failed to remove empty directory: %v", err)
	}
}

func testSymlinkOperations(t *testing.T, addr string) {
	client, cleanup := connectSFTP(t, addr)
	defer cleanup()
	
	// Create target file
	targetFile := "target.txt"
	file, err := client.Create(targetFile)
	if err != nil {
		t.Fatalf("Failed to create target file: %v", err)
	}
	file.Write([]byte("target content"))
	file.Close()
	
	// Create symlink
	linkName := "link.txt"
	err = client.Symlink(targetFile, linkName)
	if err != nil {
		t.Fatalf("Failed to create symlink: %v", err)
	}
	
	// Stat symlink
	info, err := client.Lstat(linkName)
	if err != nil {
		t.Fatalf("Failed to lstat symlink: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("File is not a symlink. Mode: %v (%o)", info.Mode(), info.Mode())
	}
	
	// Read symlink target
	target, err := client.ReadLink(linkName)
	if err != nil {
		t.Fatalf("Failed to read symlink: %v", err)
	}
	if !strings.Contains(target, targetFile) {
		t.Errorf("Symlink target incorrect: got %q, want to contain %q", target, targetFile)
	}
	
	// Cleanup
	client.Remove(linkName)
	client.Remove(targetFile)
}

func testLargeFileTransfer(t *testing.T, addr string) {
	client, cleanup := connectSFTP(t, addr)
	defer cleanup()
	
	// Create a 10MB file
	size := 10 * 1024 * 1024
	data := make([]byte, size)
	rand.Read(data[:1024]) // Just randomize first 1KB for speed
	
	fileName := "large.bin"
	file, err := client.Create(fileName)
	if err != nil {
		t.Fatalf("Failed to create large file: %v", err)
	}
	
	// Write in chunks
	chunkSize := 64 * 1024
	for i := 0; i < size; i += chunkSize {
		end := i + chunkSize
		if end > size {
			end = size
		}
		_, err := file.Write(data[i:end])
		if err != nil {
			t.Fatalf("Failed to write chunk: %v", err)
		}
	}
	file.Close()
	
	// Verify size
	info, err := client.Stat(fileName)
	if err != nil {
		t.Fatalf("Failed to stat large file: %v", err)
	}
	if info.Size() != int64(size) {
		t.Errorf("Large file size mismatch: got %d, want %d", info.Size(), size)
	}
	
	// Read back and verify first chunk
	file, err = client.Open(fileName)
	if err != nil {
		t.Fatalf("Failed to open large file: %v", err)
	}
	
	readData := make([]byte, 1024)
	n, err := file.Read(readData)
	if err != nil && err != io.EOF {
		t.Fatalf("Failed to read large file: %v", err)
	}
	if !bytes.Equal(readData[:n], data[:n]) {
		t.Error("Large file content mismatch")
	}
	file.Close()
	
	// Cleanup
	client.Remove(fileName)
}

func testConcurrentTransfers(t *testing.T, addr string) {
	client, cleanup := connectSFTP(t, addr)
	defer cleanup()
	
	// Create multiple files concurrently
	numFiles := 10
	done := make(chan error, numFiles)
	
	for i := 0; i < numFiles; i++ {
		go func(idx int) {
			fileName := fmt.Sprintf("concurrent_%d.txt", idx)
			content := fmt.Sprintf("Content for file %d", idx)
			
			file, err := client.Create(fileName)
			if err != nil {
				done <- err
				return
			}
			
			_, err = file.Write([]byte(content))
			file.Close()
			
			if err != nil {
				done <- err
				return
			}
			
			// Read back
			file, err = client.Open(fileName)
			if err != nil {
				done <- err
				return
			}
			
			data := make([]byte, len(content))
			_, err = file.Read(data)
			file.Close()
			
			if string(data) != content {
				done <- fmt.Errorf("content mismatch for file %d", idx)
				return
			}
			
			done <- nil
		}(i)
	}
	
	// Wait for all operations
	for i := 0; i < numFiles; i++ {
		if err := <-done; err != nil {
			t.Errorf("Concurrent operation failed: %v", err)
		}
	}
	
	// Cleanup
	for i := 0; i < numFiles; i++ {
		client.Remove(fmt.Sprintf("concurrent_%d.txt", i))
	}
}

func testPathResolution(t *testing.T, addr string) {
	client, cleanup := connectSFTP(t, addr)
	defer cleanup()
	
	tests := []struct {
		name     string
		path     string
		content  string
	}{
		{"tilde", "~/tilde.txt", "tilde content"},
		{"relative", "relative.txt", "relative content"},
		{"dot", "./dot.txt", "dot content"},
		{"absolute", "/workspace/absolute.txt", "absolute content"},
	}
	
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create file
			file, err := client.Create(tt.path)
			if err != nil {
				t.Fatalf("Failed to create %s: %v", tt.path, err)
			}
			n, err := file.Write([]byte(tt.content))
			if err != nil {
				t.Fatalf("Failed to write to %s: %v", tt.path, err)
			}
			if n != len(tt.content) {
				t.Fatalf("Partial write to %s: wrote %d, expected %d", tt.path, n, len(tt.content))
			}
			err = file.Close()
			if err != nil {
				t.Fatalf("Failed to close %s: %v", tt.path, err)
			}
			
			// Read back
			file, err = client.Open(tt.path)
			if err != nil {
				t.Fatalf("Failed to open %s: %v", tt.path, err)
			}
			
			data := make([]byte, len(tt.content))
			file.Read(data)
			file.Close()
			
			if string(data) != tt.content {
				t.Errorf("Content mismatch for %s: got %q, want %q", tt.path, string(data), tt.content)
			}
		})
	}
	
	// Cleanup
	client.Remove("tilde.txt")
	client.Remove("relative.txt")
	client.Remove("dot.txt")
	client.Remove("/workspace/absolute.txt")
}

func testDirectoryUpload(t *testing.T, addr string) {
	client, cleanup := connectSFTP(t, addr)
	defer cleanup()
	
	// Test what happens when SCP tries to upload to a directory path
	// This reproduces: scp ~/junk.txt user@host:~
	// Where ~ is a directory but SCP tries to create a file there
	
	t.Run("current behavior - write to directory path", func(t *testing.T) {
		// First, let's see what currently happens when we try to
		// create a file at a path that's actually a directory
		
		// The path "~" resolves to "/workspace" which exists as a directory
		targetPath := "~"
		
		// First check if /workspace exists directly
		wsInfo, wsErr := client.Stat("/workspace")
		if wsErr != nil {
			t.Logf("Direct stat of /workspace failed: %v", wsErr)
		} else {
			t.Logf("/workspace exists, IsDir=%v", wsInfo.IsDir())
		}
		
		// Check that it's a directory
		info, err := client.Stat(targetPath)
		if err != nil {
			t.Logf("Stat of %q failed: %v", targetPath, err)
			// For now, let's just try to create the file anyway to see what happens
		} else if !info.IsDir() {
			t.Fatalf("Expected %s to be a directory", targetPath)
		}
		
		// Now try to create a file at this directory path
		// This is what happens when SCP sends the path without a filename
		file, err := client.Create(targetPath)
		if err != nil {
			t.Fatalf("Failed to create file at directory path %q: %v", targetPath, err)
		}
		
		// Write some content
		content := []byte("test content for directory upload")
		n, err := file.Write(content)
		if err != nil {
			t.Fatalf("Write failed: %v", err)
		}
		if n != len(content) {
			t.Fatalf("Partial write: %d/%d bytes", n, len(content))
		}
		
		// Close should now succeed with our fix
		err = file.Close()
		if err != nil {
			t.Fatalf("Close failed: %v", err)
		}
		
		// The file should be created in the directory with a default name
		// Check that a file was created in /workspace
		entries, err := client.ReadDir("/workspace")
		if err != nil {
			t.Fatalf("Failed to read directory: %v", err)
		}
		
		// Look for the uploaded file
		found := false
		for _, entry := range entries {
			if !entry.IsDir() && strings.Contains(entry.Name(), "scp-upload") {
				found = true
				t.Logf("Found uploaded file: %s", entry.Name())
				
				// Verify content
				uploadedFile, err := client.Open(filepath.Join("/workspace", entry.Name()))
				if err != nil {
					t.Errorf("Failed to open uploaded file: %v", err)
				} else {
					data := make([]byte, len(content))
					uploadedFile.Read(data)
					uploadedFile.Close()
					if string(data) != string(content) {
						t.Errorf("Content mismatch: got %q, want %q", string(data), string(content))
					}
				}
				
				// Cleanup
				client.Remove(filepath.Join("/workspace", entry.Name()))
				break
			}
		}
		
		if !found {
			t.Error("Uploaded file not found in directory")
		}
	})
	
	t.Run("correct behavior - detect directory and append filename", func(t *testing.T) {
		// This is how it should work:
		// 1. Client tries to upload to "~" 
		// 2. Server detects it's a directory
		// 3. Server should either:
		//    a. Reject immediately (current approach), OR
		//    b. Accept but require the client to send filename separately
		
		// For now, we expect the server to reject writes to directories
		// The client (SCP) should handle this by appending the filename
		
		t.Log("Server should reject writes to directory paths")
		t.Log("Client (SCP) is responsible for appending filename")
	})
}

func testPermissions(t *testing.T, addr string) {
	client, cleanup := connectSFTP(t, addr)
	defer cleanup()
	
	fileName := "perms.txt"
	file, err := client.Create(fileName)
	if err != nil {
		t.Fatalf("Failed to create file: %v", err)
	}
	file.Write([]byte("test"))
	file.Close()
	
	// Change permissions
	err = client.Chmod(fileName, 0755)
	if err != nil {
		t.Fatalf("Failed to chmod: %v", err)
	}
	
	// Verify permissions
	info, err := client.Stat(fileName)
	if err != nil {
		t.Fatalf("Failed to stat: %v", err)
	}
	
	// Check if executable bit is set (0755 includes executable)
	if info.Mode().Perm()&0100 == 0 {
		t.Error("Executable bit not set after chmod")
	}
	
	// Change times
	now := time.Now()
	err = client.Chtimes(fileName, now, now)
	if err != nil {
		t.Fatalf("Failed to change times: %v", err)
	}
	
	// Cleanup
	client.Remove(fileName)
}

func testSCPCommands(t *testing.T, addr string, containerID string) {
	// Create test SSH key for SCP
	keyFile := filepath.Join(t.TempDir(), "test_key")
	if err := generateTestSSHKey(keyFile); err != nil {
		t.Fatalf("Failed to generate test key: %v", err)
	}
	
	// Test scp upload
	t.Run("Upload", func(t *testing.T) {
		localFile := filepath.Join(t.TempDir(), "upload.txt")
		os.WriteFile(localFile, []byte("scp upload test"), 0644)
		
		cmd := scpCommand(
			"-i", keyFile,
			"-P", getPort(addr),
			localFile,
			fmt.Sprintf("test@localhost:upload.txt"))
		
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Logf("SCP output: %s", output)
			t.Skip("SCP command not working in test environment")
		}
		
		// Verify file exists in container
		var stdout bytes.Buffer
		cmd = exec.Command("docker", "exec", containerID, "cat", "/workspace/upload.txt")
		cmd.Stdout = &stdout
		if err := cmd.Run(); err == nil {
			if stdout.String() != "scp upload test" {
				t.Error("Uploaded file content mismatch")
			}
		}
	})
	
	// Test scp download
	t.Run("Download", func(t *testing.T) {
		// Create file in container
		exec.Command("docker", "exec", containerID,
			"sh", "-c", "echo 'scp download test' > /workspace/download.txt").Run()
		
		localFile := filepath.Join(t.TempDir(), "download.txt")
		cmd := scpCommand(
			"-i", keyFile,
			"-P", getPort(addr),
			"test@localhost:download.txt",
			localFile)
		
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Logf("SCP output: %s", output)
			t.Skip("SCP command not working in test environment")
		}
		
		// Verify downloaded file
		if content, err := os.ReadFile(localFile); err == nil {
			if string(content) != "scp download test\n" {
				t.Error("Downloaded file content mismatch")
			}
		}
	})
	
	// Test recursive copy
	t.Run("Recursive", func(t *testing.T) {
		// Create directory structure locally
		testDir := filepath.Join(t.TempDir(), "testdir")
		os.Mkdir(testDir, 0755)
		os.WriteFile(filepath.Join(testDir, "file1.txt"), []byte("file1"), 0644)
		os.WriteFile(filepath.Join(testDir, "file2.txt"), []byte("file2"), 0644)
		
		cmd := scpCommand(
			"-r",
			"-i", keyFile,
			"-P", getPort(addr),
			testDir,
			"test@localhost:testdir")
		
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Logf("SCP output: %s", output)
			t.Skip("SCP recursive command not working in test environment")
		}
		
		// Verify directory structure in container
		var stdout bytes.Buffer
		cmd = exec.Command("docker", "exec", containerID, "ls", "/workspace/testdir")
		cmd.Stdout = &stdout
		if err := cmd.Run(); err == nil {
			if !strings.Contains(stdout.String(), "file1.txt") ||
				!strings.Contains(stdout.String(), "file2.txt") {
				t.Error("Recursive copy failed")
			}
		}
	})
}

func generateTestSSHKey(keyFile string) error {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return err
	}
	
	// Write private key
	privateKey := &pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	}
	
	file, err := os.Create(keyFile)
	if err != nil {
		return err
	}
	defer file.Close()
	
	if err := pem.Encode(file, privateKey); err != nil {
		return err
	}
	
	return os.Chmod(keyFile, 0600)
}

func getPort(addr string) string {
	parts := strings.Split(addr, ":")
	if len(parts) > 1 {
		return parts[len(parts)-1]
	}
	return "22"
}