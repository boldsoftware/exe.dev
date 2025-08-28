package container

import (
	"context"
	"os/exec"
	"testing"
	"time"
)

func TestContainerCommandIntegration(t *testing.T) {
	// Skip if Docker is not available
	cmd := exec.Command("docker", "version")
	if err := cmd.Run(); err != nil {
		t.Skip("Docker not available, skipping integration test")
	}

	// Test with simple ubuntu image that has a shell as default command
	manager := &DockerManager{}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	tests := []struct {
		name     string
		image    string
		cmdFlag  string
		expected []string
	}{
		{"nginx with auto should use nginx command", "nginx:alpine", "auto", []string{"nginx", "-g", "daemon off;"}},
		{"ubuntu with none should use tail", "ubuntu:22.04", "none", []string{"tail", "-f", "/dev/null"}},
		{"ubuntu with auto should detect shell and use tail", "ubuntu:22.04", "auto", []string{"tail", "-f", "/dev/null"}},
		{"redis with auto should use redis command", "redis:alpine", "auto", []string{"redis-server"}},
		{"custom python command", "python:3.9-slim", "python -c 'print(\"hello\")'", []string{"python", "-c", "print(\"hello\")"}},
		{"custom quoted command", "ubuntu:22.04", "echo 'hello world'", []string{"echo", "hello world"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Pull the image first to avoid timeouts
			pullCmd := exec.CommandContext(ctx, "docker", "pull", tt.image)
			if err := pullCmd.Run(); err != nil {
				t.Skipf("Failed to pull image %s: %v", tt.image, err)
			}

			cmd, err := manager.determineContainerCommand(ctx, tt.image, "", tt.cmdFlag)
			if err != nil {
				t.Fatalf("Failed to determine command: %v", err)
			}

			t.Logf("Image %s with flag %s resulted in command: %v", tt.image, tt.cmdFlag, cmd)

			// Verify the command matches expectations
			if len(cmd) == 0 {
				t.Error("Command should not be empty")
				return
			}

			// Check if command matches expected result
			if len(cmd) != len(tt.expected) {
				t.Errorf("Command length mismatch. Got %v, want %v", cmd, tt.expected)
				return
			}

			for i, part := range cmd {
				if part != tt.expected[i] {
					t.Errorf("Command[%d] = %q; want %q. Full command: %v vs %v", i, part, tt.expected[i], cmd, tt.expected)
					break
				}
			}
		})
	}
}
