package sshproxy

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestDebugProduction helps debug why production is failing
func TestDebugProduction(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping debug test")
	}

	// Create a real container
	cmd := exec.Command("docker", "run", "-d", "--rm", "ubuntu:22.04",
		"sh", "-c", "mkdir -p /workspace && chmod 755 /workspace && sleep 300")
	output, _ := cmd.Output()
	containerID := strings.TrimSpace(string(output))
	if containerID == "" {
		t.Skip("Failed to create container")
	}
	defer exec.Command("docker", "kill", containerID).Run()
	time.Sleep(500 * time.Millisecond)

	manager := &DockerContainerManager{containerID: containerID}
	ctx := context.Background()
	fs := NewUnixContainerFS(manager, "test", containerID, "/workspace")

	// Test what the handler actually does with various paths
	handler := NewSFTPHandler(ctx, fs, "/workspace")

	testPaths := []string{
		"/",
		"/test.txt",
		"/workspace/test.txt",
		"test.txt",
		"~",
		"~/test.txt",
	}

	t.Log("=== Path Resolution Debug ===")
	for _, path := range testPaths {
		resolved := handler.resolvePath(path)
		t.Logf("  %q → %q", path, resolved)
	}

	// Now test actual file operations
	t.Log("\n=== File Operations Test ===")
	
	// This is what happens when SCP sends /test.txt
	testPath := "/test.txt"
	resolvedPath := handler.resolvePath(testPath)
	t.Logf("Testing write to %q (resolved to %q)", testPath, resolvedPath)
	
	// Try to create the file
	file, err := fs.OpenFile(ctx, resolvedPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		t.Fatalf("OpenFile failed: %v", err)
	}
	
	// Write data
	_, err = file.Write([]byte("test"))
	if err != nil {
		t.Fatalf("Write failed: %v", err)
	}
	
	// Close - this is where it might fail
	err = file.Close()
	if err != nil {
		t.Fatalf("Close failed: %v", err)
	}
	
	t.Log("✓ File operation succeeded")
	
	// Verify file was created
	var out strings.Builder
	cmd = exec.Command("docker", "exec", containerID, "ls", "-la", "/workspace")
	cmd.Stdout = &out
	cmd.Run()
	t.Logf("Files in /workspace:\n%s", out.String())
}