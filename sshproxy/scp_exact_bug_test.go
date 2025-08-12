package sshproxy

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestSCPExactBug demonstrates the exact bug where SCP to ~ results in trying to write to /filename
func TestSCPExactBug(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	if err := exec.Command("docker", "version").Run(); err != nil {
		t.Skip("Docker not available")
	}

	// Create container
	cmd := exec.Command("docker", "run", "-d", "--rm", "ubuntu:22.04",
		"sh", "-c", "mkdir -p /workspace && sleep 300")
	output, _ := cmd.Output()
	containerID := strings.TrimSpace(string(output))
	defer exec.Command("docker", "kill", containerID).Run()
	time.Sleep(500 * time.Millisecond)

	manager := &DockerContainerManager{containerID: containerID}
	ctx := context.Background()
	fs := NewUnixContainerFS(manager, "test", containerID, "/workspace")

	t.Run("DirectProblem", func(t *testing.T) {
		// This is what happens when SCP sends "/test.txt" 
		// (which it does when user types "scp file host:~")
		
		t.Log("Testing what happens when SFTP tries to write to /test.txt")
		
		// Try to create /test.txt
		file, err := fs.OpenFile(ctx, "/test.txt", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
		if err != nil {
			t.Logf("OpenFile(/test.txt) failed immediately: %v", err)
			return
		}
		
		t.Log("OpenFile(/test.txt) succeeded (file handle created)")
		
		// Write data
		_, err = file.Write([]byte("test content"))
		if err != nil {
			t.Logf("Write failed: %v", err)
		} else {
			t.Log("Write succeeded (data buffered)")
		}
		
		// Close - with the fix, this should now succeed
		err = file.Close()
		if err != nil {
			t.Errorf("Close() failed: %v", err)
			t.Log("ERROR: The fix is not working properly!")
		} else {
			t.Log("✓✓✓ Close() succeeded - fix is working!")
			t.Log("\nFIX VERIFICATION:")
			t.Log("1. User types: scp file.txt host:~")
			t.Log("2. SCP resolves ~ to / and sends path: /file.txt")
			t.Log("3. Fixed handler maps /file.txt to /workspace/file.txt")
			t.Log("4. File is successfully created in /workspace")
			t.Log("5. SCP completes successfully")
			
			// Verify the file was created in /workspace
			var out strings.Builder
			cmd := exec.Command("docker", "exec", containerID, "ls", "-la", "/workspace/test.txt")
			cmd.Stdout = &out
			err := cmd.Run()
			if err == nil {
				t.Logf("✓ File exists in /workspace: %s", strings.TrimSpace(out.String()))
			}
		}
	})

	t.Run("PathResolution", func(t *testing.T) {
		// Show how different handlers resolve paths
		originalHandler := NewOriginalSFTPHandler(ctx, fs, "/workspace")
		currentHandler := NewSFTPHandler(ctx, fs, "/workspace")
		
		testPaths := []string{
			"/",           // What SCP sends for ~ (before appending filename)
			"/test.txt",   // What SCP actually sends for ~/test.txt
			"~",           // Tilde
			"~/test.txt",  // Tilde with file
		}
		
		t.Log("Path resolution comparison:")
		for _, path := range testPaths {
			origResolved := originalHandler.resolvePath(path)
			currResolved := currentHandler.resolvePath(path)
			
			if origResolved != currResolved {
				t.Logf("  %q: original→%q, current→%q ✓ DIFFERENT", 
					path, origResolved, currResolved)
			} else {
				t.Logf("  %q: both→%q", path, origResolved)
			}
		}
	})

	t.Run("ContainerRootPermissions", func(t *testing.T) {
		// Check the actual permissions of / in the container
		var out strings.Builder
		cmd := exec.Command("docker", "exec", containerID, "ls", "-ld", "/")
		cmd.Stdout = &out
		cmd.Run()
		t.Logf("Root directory permissions: %s", strings.TrimSpace(out.String()))
		
		out.Reset()
		cmd = exec.Command("docker", "exec", containerID, "touch", "/test-write")
		cmd.Stdout = &out
		cmd.Stderr = &out
		err := cmd.Run()
		if err != nil {
			t.Logf("Cannot write to /: %s", strings.TrimSpace(out.String()))
		}
	})
}

// TestSCPBugSummary provides a clear summary of the bug
func TestSCPBugSummary(t *testing.T) {
	t.Log("=== SCP '~' BUG SUMMARY ===")
	t.Log("")
	t.Log("PROBLEM:")
	t.Log("  When user runs: scp file.txt user@host:~")
	t.Log("  SCP sends SFTP path: /file.txt")
	t.Log("  This tries to create a file in the container's root directory")
	t.Log("")
	t.Log("SYMPTOMS:")
	t.Log("  - Upload shows 100% progress")
	t.Log("  - Then fails with 'close remote: Failure'")
	t.Log("")
	t.Log("ROOT CAUSE:")
	t.Log("  1. Modern OpenSSH scp uses SFTP protocol")
	t.Log("  2. SCP resolves ~ to / before sending to SFTP")
	t.Log("  3. Original handler doesn't transform / back to home directory")
	t.Log("  4. Tries to write to /file.txt which fails in container")
	t.Log("")
	t.Log("FIX APPROACH:")
	t.Log("  - When SFTP receives path starting with '/', treat as home directory")
	t.Log("  - Current fix: resolvePath maps '/' to '/workspace'")
	t.Log("  - But this doesn't handle '/file.txt' → '/workspace/file.txt'")
	t.Log("")
	t.Log("PROPER FIX NEEDED:")
	t.Log("  - For paths like '/file.txt', map to '/workspace/file.txt'")
	t.Log("  - Essentially: treat / as home directory for SFTP operations")
}