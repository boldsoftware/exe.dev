package container

import (
	"context"
	"testing"
)

// TestListAllocs tests the ListAllocs functionality
func TestListAllocs(t *testing.T) {
	ctx := context.Background()

	// Create container manager with local Docker
	config := &Config{
		ContainerdAddresses: []string{"local"},
		KataAnnotations:     map[string]string{},
	}

	manager, err := NewNerdctlManager(config)
	if err != nil {
		t.Skipf("Failed to create container manager: %v", err)
	}

	// Create test allocations
	testAllocs := []struct {
		allocID string
		ipRange string
	}{
		{"list-test-1", "10.42.200.0/24"},
		{"list-test-2", "10.42.201.0/24"},
		{"list-test-3", "10.42.202.0/24"},
	}

	// Create allocations
	for _, alloc := range testAllocs {
		err := manager.CreateAlloc(ctx, alloc.allocID, alloc.ipRange)
		if err != nil {
			t.Fatalf("Failed to create allocation %s: %v", alloc.allocID, err)
		}
		defer manager.DeleteAlloc(ctx, alloc.allocID, "local") // Cleanup
	}

	// List allocations
	allocsOnHost, err := manager.ListAllocs(ctx, "local")
	if err != nil {
		t.Fatalf("Failed to list allocations: %v", err)
	}

	// Verify all test allocations are listed
	for _, testAlloc := range testAllocs {
		found := false
		truncatedID := testAlloc.allocID
		if len(truncatedID) > 12 {
			truncatedID = truncatedID[:12]
		}

		for _, allocID := range allocsOnHost {
			if allocID == truncatedID {
				found = true
				break
			}
		}

		if !found {
			t.Errorf("Allocation %s not found in list", testAlloc.allocID)
		}
	}
}

// TestDeleteAlloc tests the DeleteAlloc functionality
func TestDeleteAlloc(t *testing.T) {
	ctx := context.Background()

	// Create container manager with local Docker
	config := &Config{
		ContainerdAddresses: []string{"local"},
		KataAnnotations:     map[string]string{},
	}

	manager, err := NewNerdctlManager(config)
	if err != nil {
		t.Skipf("Failed to create container manager: %v", err)
	}

	// Create test allocation
	allocID := "delete-test"
	ipRange := "10.42.203.0/24"

	err = manager.CreateAlloc(ctx, allocID, ipRange)
	if err != nil {
		t.Fatalf("Failed to create allocation: %v", err)
	}

	// Verify it exists
	allocsBeforeDelete, err := manager.ListAllocs(ctx, "local")
	if err != nil {
		t.Fatalf("Failed to list allocations: %v", err)
	}

	found := false
	truncatedID := allocID
	if len(truncatedID) > 12 {
		truncatedID = truncatedID[:12]
	}

	for _, id := range allocsBeforeDelete {
		if id == truncatedID {
			found = true
			break
		}
	}

	if !found {
		t.Fatalf("Allocation not found before delete")
	}

	// Delete the allocation
	err = manager.DeleteAlloc(ctx, allocID, "local")
	if err != nil {
		t.Fatalf("Failed to delete allocation: %v", err)
	}

	// Verify it's deleted
	allocsAfterDelete, err := manager.ListAllocs(ctx, "local")
	if err != nil {
		t.Fatalf("Failed to list allocations after delete: %v", err)
	}

	for _, id := range allocsAfterDelete {
		if id == truncatedID {
			t.Errorf("Allocation still exists after delete")
		}
	}
}

// TestAllocSyncWithOrphans tests that orphaned allocations are removed during sync
func TestAllocSyncWithOrphans(t *testing.T) {
	ctx := context.Background()

	// Create container manager with local Docker
	config := &Config{
		ContainerdAddresses: []string{"local"},
		KataAnnotations:     map[string]string{},
	}

	manager, err := NewNerdctlManager(config)
	if err != nil {
		t.Skipf("Failed to create container manager: %v", err)
	}

	// Create an orphaned allocation (not tracked in database)
	orphanID := "orphan-test"
	orphanIPRange := "10.42.204.0/24"

	err = manager.CreateAlloc(ctx, orphanID, orphanIPRange)
	if err != nil {
		t.Fatalf("Failed to create orphan allocation: %v", err)
	}

	// Verify orphan exists
	allocsBefore, err := manager.ListAllocs(ctx, "local")
	if err != nil {
		t.Fatalf("Failed to list allocations: %v", err)
	}

	foundOrphan := false
	truncatedOrphanID := orphanID
	if len(truncatedOrphanID) > 12 {
		truncatedOrphanID = truncatedOrphanID[:12]
	}

	for _, id := range allocsBefore {
		if id == truncatedOrphanID {
			foundOrphan = true
			break
		}
	}

	if !foundOrphan {
		t.Fatalf("Orphan allocation not found")
	}

	// Delete the orphan
	err = manager.DeleteAlloc(ctx, orphanID, "local")
	if err != nil {
		t.Fatalf("Failed to delete orphan allocation: %v", err)
	}

	// Verify orphan is deleted
	allocsAfter, err := manager.ListAllocs(ctx, "local")
	if err != nil {
		t.Fatalf("Failed to list allocations after delete: %v", err)
	}

	for _, id := range allocsAfter {
		if id == truncatedOrphanID {
			t.Errorf("Orphan allocation still exists after delete")
		}
	}
}
