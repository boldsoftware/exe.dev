package sshproxy

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestSimpleSCPBug demonstrates the exact failure by checking what happens
// when we try to write a file to the path that "~" resolves to
func TestSimpleSCPBug(t *testing.T) {
	// When SCP user types: scp file.txt host:~
	// SCP does the following:
	// 1. Sends SFTP stat for "/"
	// 2. Sees it's a directory
	// 3. Tries to create a file at "/" (the resolved path of ~)
	
	// This test simulates that exact scenario
	t.Log("Simulating what happens when SCP tries to upload to ~")
	
	// Create a real container manager
	if err := exec.Command("docker", "version").Run(); err != nil {
		t.Skip("Docker not available")
	}
	
	// Create container
	cmd := exec.Command("docker", "run", "-d", "--rm", "alpine", "sleep", "10")
	out, err := cmd.Output()
	if err != nil {
		t.Skip("Failed to create container")
	}
	containerID := strings.TrimSpace(string(out))
	defer exec.Command("docker", "kill", containerID).Run()
	
	// Create /workspace
	exec.Command("docker", "exec", containerID, "mkdir", "-p", "/workspace").Run()
	
	manager := &DockerContainerManager{containerID: containerID}
	ctx := context.Background()
	
	// Create filesystem with /workspace as home
	fs := NewUnixContainerFS(manager, "user", containerID, "/workspace")
	
	// First, stat "/" to see if it's a directory
	info, err := fs.Stat(ctx, "/")
	if err != nil {
		t.Logf("Stat(/) failed: %v", err)
	} else {
		t.Logf("Stat(/) succeeded, IsDir=%v", info.IsDir())
	}
	
	// Now try to create a file at "/" - this is what causes the bug
	t.Log("Attempting to create file at '/' (this should fail)")
	file, err := fs.OpenFile(ctx, "/", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		t.Logf("OpenFile(/) failed immediately: %v", err)
		return
	}
	
	// Write data
	_, err = file.Write([]byte("test content"))
	if err != nil {
		t.Logf("Write failed: %v", err)
		return
	}
	
	// Close - THIS is where the failure happens
	err = file.Close()
	if err != nil {
		t.Logf("✓ Close() failed as expected: %v", err)
		
		// Check if the error message matches what we see in production
		if strings.Contains(err.Error(), "write failed") {
			t.Log("✓ Error contains 'write failed'")
		}
		
		// In production, this error gets translated to "scp: close remote: Failure"
		t.Log("✓ This error causes 'scp: close remote: Failure' in SCP")
		
		// This test PASSES by demonstrating the bug
		t.Log("BUG CONFIRMED: Cannot write file to / path")
	} else {
		t.Error("Close() succeeded - expected failure when writing to /")
	}
}