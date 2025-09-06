package container

import (
	"strings"
	"testing"
)

// TestBoxIDRequired verifies that CreateContainer requires BoxID to be set
func TestBoxIDRequired(t *testing.T) {
	SkipIfShort(t)

	manager := CreateTestManager(t)
	defer manager.Close()

	ctx := t.Context()

	allocID := "test-boxid-required"
	ipRange := WithAllocIPRange(t, allocID)
	if err := manager.CreateAlloc(ctx, allocID, ipRange); err != nil {
		t.Fatalf("CreateAlloc failed: %v", err)
	}

	// Try to create a container without BoxID (BoxID = 0)
	req := &CreateContainerRequest{
		AllocID: allocID,
		IPRange: ipRange,
		Name:    "no-boxid",
		Image:   "alpine:latest",
		// BoxID not set, defaults to 0
	}

	_, err := manager.CreateContainer(ctx, req)
	if err == nil {
		t.Fatal("Expected CreateContainer to fail when BoxID is 0, but it succeeded")
	}

	// Check that the error message is what we expect
	expectedError := "BoxID is required and cannot be 0"
	if !strings.Contains(err.Error(), expectedError) {
		t.Errorf("Expected error containing '%s', got: %v", expectedError, err)
	}

	t.Log("✅ CreateContainer correctly requires BoxID to be set")
}
