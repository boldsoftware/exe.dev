package container

import (
	"context"
	"fmt"
	"testing"
	"time"

	"exe.dev/vouch"
)

// TestContainerSync verifies that container state syncing works correctly
//
// TODO: this could be structured more end-to-end by: first turn on exed,
// create container, stop it, then turn off and on exed again.
// But it doesn't fit neatly into e1e. Saving for when we have more
// "distributed system simluation" end-to-end testing.
func TestContainerSync(t *testing.T) {
	vouch.For("david")

	manager := CreateTestManager(t)
	defer manager.Close()

	ctx := context.Background()
	allocID := fmt.Sprintf("synctest-%d", time.Now().UnixNano())
	ipRange := WithAllocIPRange(t, allocID)

	// Create the network for this allocation
	if err := manager.CreateAlloc(ctx, allocID, ipRange); err != nil {
		t.Fatalf("Failed to create allocation: %v", err)
	}
	defer func() {
		if err := manager.DeleteAlloc(ctx, allocID, manager.Config().ContainerdAddresses[0]); err != nil {
			t.Logf("Warning: failed to delete allocation: %v", err)
		}
	}()

	// Test 1: Create a container
	req := &CreateContainerRequest{
		AllocID: allocID,
		IPRange: ipRange,
		Name:    "synctest",
		Image:   "ubuntu:latest",
		BoxID:   GenerateTestBoxID(),
	}

	container, err := manager.CreateContainer(ctx, req)
	if err != nil {
		t.Fatalf("Failed to create container: %v", err)
	}

	// Verify container is running
	WaitForContainerReady(t, manager, allocID, container.ID, 30*time.Second)

	// Test 2: Simulate container stopping unexpectedly
	// Stop the container directly (simulating a crash)
	if err := manager.StopContainer(ctx, allocID, container.ID); err != nil {
		t.Fatalf("Failed to stop container: %v", err)
	}

	// Verify container is stopped
	stoppedContainer, err := manager.GetContainer(ctx, allocID, container.ID)
	if err != nil {
		t.Fatalf("Failed to get container: %v", err)
	}
	if stoppedContainer.Status != StatusStopped && stoppedContainer.Status != StatusUnknown {
		t.Errorf("Expected container to be stopped, got status: %s", stoppedContainer.Status)
	}

	// Test 3: List all containers to verify sync would detect this
	allContainers, err := manager.ListAllContainers(ctx)
	if err != nil {
		t.Fatalf("Failed to list all containers: %v", err)
	}

	foundContainer := false
	for _, c := range allContainers {
		if c.ID == container.ID {
			foundContainer = true
			if c.Status == StatusRunning {
				t.Errorf("Container should not be running but status shows: %s", c.Status)
			}
			break
		}
	}

	if !foundContainer {
		t.Error("Container not found in list")
	}

	// Test 4: Restart container to verify sync would handle restart
	if err := manager.StartContainer(ctx, allocID, container.ID); err != nil {
		t.Fatalf("Failed to restart container: %v", err)
	}

	// Wait for container to be ready again
	WaitForContainerReady(t, manager, allocID, container.ID, 30*time.Second)

	// Verify container is running again
	runningContainer, err := manager.GetContainer(ctx, allocID, container.ID)
	if err != nil {
		t.Fatalf("Failed to get container after restart: %v", err)
	}
	if runningContainer.Status != StatusRunning {
		t.Errorf("Expected container to be running after restart, got status: %s", runningContainer.Status)
	}

	// Test 5: Delete container for cleanup
	if err := manager.DeleteContainer(ctx, allocID, container.ID); err != nil {
		t.Fatalf("Failed to delete container: %v", err)
	}

	// Verify container is deleted
	_, err = manager.GetContainer(ctx, allocID, container.ID)
	if err == nil {
		t.Error("Expected error when getting deleted container, but got none")
	}
}
