package container

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestMultipleDeletionsWithSameBoxID verifies that deleting multiple containers
// with the same box ID correctly handles existing deleted disks by appending timestamps
func TestMultipleDeletionsWithSameBoxID(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)

	manager := CreateTestManager(t)
	defer manager.Close()

	ctx := t.Context()

	// Create allocation with unique ID to avoid conflicts
	allocID := fmt.Sprintf("test-multi-delete-%d", time.Now().Unix())

	// Use a specific BoxID for all iterations
	boxID := GenerateTestBoxID()
	host, releaseFn, err := manager.selectHost(ctx, allocID)
	if err != nil {
		t.Fatalf("selectHost failed: %v", err)
	}
	releaseFn()

	// First deletion - should go to /data/exed/deleted/box-<id>
	t.Log("=== First container creation and deletion ===")
	req1 := &CreateContainerRequest{
		AllocID: allocID,
		Name:    "first-container",
		Image:   "alpine:latest",
		Size:    "small",
		BoxID:   boxID,
	}

	container1, err := manager.CreateContainer(ctx, req1)
	if err != nil {
		t.Fatalf("First CreateContainer failed: %v", err)
	}

	// Wait for container to be running
	time.Sleep(3 * time.Second)

	// Delete the first container
	if err := manager.DeleteContainer(ctx, allocID, container1.ID); err != nil {
		t.Fatalf("First DeleteContainer failed: %v", err)
	}

	// Verify the disk was moved to /data/exed/deleted/box-<id>
	firstDeletedPath := manager.DataPath(fmt.Sprintf("exed/deleted/box-%d", boxID))
	checkFirstCmd := manager.ExecSSHCommand(ctx, host, "test", "-d", firstDeletedPath)
	if err := checkFirstCmd.Run(); err != nil {
		t.Fatalf("First deleted disk not found at %s", firstDeletedPath)
	}
	t.Logf("✅ First deletion: disk moved to %s", firstDeletedPath)

	// Second deletion - should go to /data/exed/deleted/box-<id>-<timestamp>
	t.Log("=== Second container creation and deletion ===")
	req2 := &CreateContainerRequest{
		AllocID: allocID,
		Name:    "second-container",
		Image:   "alpine:latest",
		Size:    "small",
		BoxID:   boxID,
	}

	container2, err := manager.CreateContainer(ctx, req2)
	if err != nil {
		t.Fatalf("Second CreateContainer failed: %v", err)
	}

	// Wait for container to be running
	time.Sleep(3 * time.Second)

	// Delete the second container
	if err := manager.DeleteContainer(ctx, allocID, container2.ID); err != nil {
		t.Fatalf("Second DeleteContainer failed: %v", err)
	}

	// Verify the first deleted disk still exists
	checkFirstStillCmd := manager.ExecSSHCommand(ctx, host, "test", "-d", firstDeletedPath)
	if err := checkFirstStillCmd.Run(); err != nil {
		t.Errorf("First deleted disk disappeared from %s", firstDeletedPath)
	} else {
		t.Logf("✅ First deleted disk still exists at %s", firstDeletedPath)
	}

	// Find the timestamped second deletion
	lsCmd := manager.ExecSSHCommand(ctx, host, "ls", "-la", manager.DataPath("exed/deleted/"))
	output, err := lsCmd.Output()
	if err != nil {
		t.Fatalf("Failed to list deleted directory: %v", err)
	}

	// Look for a timestamped version
	foundTimestamped := false
	lines := strings.Split(string(output), "\n")
	timestampPrefix := fmt.Sprintf("box-%d-", boxID)
	for _, line := range lines {
		if strings.Contains(line, timestampPrefix) {
			// Extract the filename from ls output
			parts := strings.Fields(line)
			if len(parts) > 0 {
				filename := parts[len(parts)-1]
				if strings.HasPrefix(filename, timestampPrefix) {
					t.Logf("✅ Second deletion: found timestamped disk %s", filename)
					foundTimestamped = true

					// Verify it's a recent timestamp (within last minute)
					timestampPart := strings.TrimPrefix(filename, timestampPrefix)
					if len(timestampPart) == 15 { // YYYYMMDD-HHMMSS format
						// Basic validation that it looks like a timestamp
						if timestampPart[8] == '-' {
							t.Logf("  Timestamp format looks correct: %s", timestampPart)
						}
					}
					break
				}
			}
		}
	}

	if !foundTimestamped {
		t.Errorf("Did not find timestamped deleted disk for second deletion")
		t.Logf("Directory listing:\n%s", output)
	}

	// Third deletion - should create another timestamped version
	t.Log("=== Third container creation and deletion ===")

	// Sleep 1 second to ensure different timestamp
	time.Sleep(1 * time.Second)

	req3 := &CreateContainerRequest{
		AllocID: allocID,
		Name:    "third-container",
		Image:   "alpine:latest",
		Size:    "small",
		BoxID:   boxID,
	}

	container3, err := manager.CreateContainer(ctx, req3)
	if err != nil {
		t.Fatalf("Third CreateContainer failed: %v", err)
	}

	// Wait for container to be running
	time.Sleep(3 * time.Second)

	// Delete the third container
	if err := manager.DeleteContainer(ctx, allocID, container3.ID); err != nil {
		t.Fatalf("Third DeleteContainer failed: %v", err)
	}

	// List directory again and verify we have 3 deleted disks
	lsCmd2 := manager.ExecSSHCommand(ctx, host, "ls", manager.DataPath("exed/deleted/"))
	output2, err := lsCmd2.Output()
	if err != nil {
		t.Fatalf("Failed to list deleted directory: %v", err)
	}

	// Count how many deleted disks we have for this box ID
	lines2 := strings.Split(string(output2), "\n")
	count := 0
	boxPrefix := fmt.Sprintf("box-%d", boxID)
	for _, line := range lines2 {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, boxPrefix) {
			count++
			t.Logf("  Found deleted disk: %s", line)
		}
	}

	if count != 3 {
		t.Errorf("Expected 3 deleted disks for box %d, found %d", boxID, count)
		t.Logf("Directory listing:\n%s", output2)
	} else {
		t.Logf("✅ All 3 deleted disks are preserved with proper naming")
	}

	// Clean up test artifacts
	cleanupCmd := manager.ExecSSHCommand(ctx, host, "rm", "-rf",
		manager.DataPath(fmt.Sprintf("exed/deleted/box-%d*", boxID)))
	_ = cleanupCmd.Run()

	t.Log("✅ Multiple deletions with same box ID work correctly")
}
