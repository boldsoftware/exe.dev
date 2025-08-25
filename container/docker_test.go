package container

import (
	"context"
	"testing"
)

func TestIsShellCommand(t *testing.T) {
	tests := []struct {
		name     string
		cmd      []string
		expected bool
	}{
		{"empty command", []string{}, false},
		{"bash", []string{"bash"}, true},
		{"sh", []string{"sh"}, true},
		{"zsh with args", []string{"zsh", "-c", "echo hello"}, true},
		{"bash with path", []string{"/bin/bash"}, true},
		{"dash with path", []string{"/usr/bin/dash", "-l"}, true},
		{"python", []string{"python"}, false},
		{"node", []string{"node", "app.js"}, false},
		{"nginx", []string{"nginx", "-g", "daemon off;"}, false},
		{"fish shell", []string{"fish"}, true},
		{"csh shell", []string{"csh"}, true},
		{"tcsh shell", []string{"tcsh"}, true},
		{"ksh shell", []string{"ksh"}, true},
		{"not shell with shell substring", []string{"myshell-app"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isShellCommand(tt.cmd)
			if result != tt.expected {
				t.Errorf("isShellCommand(%v) = %v; want %v", tt.cmd, result, tt.expected)
			}
		})
	}
}

func TestDetermineContainerCommand(t *testing.T) {
	manager := &DockerManager{}
	ctx := context.Background()

	tests := []struct {
		name            string
		commandOverride string
		expected        []string
	}{
		{"none override", "none", []string{"tail", "-f", "/dev/null"}},
		{"custom command", "python app.py", []string{"python", "app.py"}},
		{"empty custom command", "", []string{"tail", "-f", "/dev/null"}},
		{"single word custom", "nginx", []string{"nginx"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Skip auto tests since they require actual Docker image inspection
			if tt.commandOverride == "auto" {
				t.Skip("Skipping auto tests - requires Docker image")
			}

			result, err := manager.determineContainerCommand(ctx, "ubuntu", "", tt.commandOverride)
			if err != nil {
				t.Fatalf("determineContainerCommand failed: %v", err)
			}

			if len(result) != len(tt.expected) {
				t.Errorf("Command length mismatch. Got %v, want %v", result, tt.expected)
				return
			}

			for i, cmd := range result {
				if cmd != tt.expected[i] {
					t.Errorf("Command[%d] = %q; want %q", i, cmd, tt.expected[i])
				}
			}
		})
	}
}

func TestCustomCommandParsing(t *testing.T) {
	manager := &DockerManager{}
	ctx := context.Background()

	tests := []struct {
		name            string
		commandOverride string
		expected        []string
	}{
		{"simple command", "python app.py", []string{"python", "app.py"}},
		{"quoted arguments", "python -c 'print(\"hello world\")'", []string{"python", "-c", "print(\"hello world\")"}},
		{"mixed quotes", "echo 'hello' \"world\"", []string{"echo", "hello", "world"}},
		{"complex command", "bash -c 'cd /app && python main.py'", []string{"bash", "-c", "cd /app && python main.py"}},
		{"single word", "nginx", []string{"nginx"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := manager.determineContainerCommand(ctx, "ubuntu", "", tt.commandOverride)
			if err != nil {
				t.Fatalf("determineContainerCommand failed: %v", err)
			}

			if len(result) != len(tt.expected) {
				t.Errorf("Command length mismatch. Got %v, want %v", result, tt.expected)
				return
			}

			for i, cmd := range result {
				if cmd != tt.expected[i] {
					t.Errorf("Command[%d] = %q; want %q", i, cmd, tt.expected[i])
				}
			}
		})
	}
}
