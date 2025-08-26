package container

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestContainerExecuteInContainer(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)

	// Detect which container backend to use
	backend := GetTestBackend(t)
	manager := CreateTestManager(t, backend)
	defer manager.Close()

	// Create a test container
	ctx := context.Background()
	// Use a unique name to avoid conflicts
	containerName := fmt.Sprintf("test-ssh-%d", time.Now().UnixNano())
	req := &CreateContainerRequest{
		AllocID: "test-alloc",
		Name:    containerName,
		Image:   "ubuntu:22.04",
	}

	container, err := manager.CreateContainer(ctx, req)
	if err != nil {
		t.Fatalf("Failed to create container: %v", err)
	}
	defer CleanupContainer(t, manager, "test-alloc", container.ID)

	// Test 1: Simple command execution without PTY
	t.Run("SimpleExec", func(t *testing.T) {
		var stdout bytes.Buffer
		err := manager.ExecuteInContainer(ctx, "test-alloc", container.ID,
			[]string{"echo", "hello"},
			nil, &stdout, nil)
		if err != nil {
			t.Errorf("Failed to execute simple command: %v", err)
		}
		if out := stdout.String(); !strings.Contains(out, "hello") {
			t.Errorf("Expected 'hello' in output, got: %s", out)
		}
	})

	// Test 2: Command execution with stdin/stdout (simulating SSH)
	t.Run("InteractiveExec", func(t *testing.T) {
		stdin := strings.NewReader("echo 'interactive test'\nexit\n")
		var stdout bytes.Buffer

		err := manager.ExecuteInContainer(ctx, "test-alloc", container.ID,
			[]string{"/bin/bash"},
			stdin, &stdout, nil)

		// The command might fail due to PTY handling, but should not give "input device is not a TTY"
		if err != nil && strings.Contains(err.Error(), "input device is not a TTY") {
			t.Errorf("Got TTY error when it should be handled: %v", err)
		}
	})
}
