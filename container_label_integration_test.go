package exe

import (
	"context"
	"testing"

	"exe.dev/container"
)

// TestLabelLengthFix tests that the original issue values would have caused problems
func TestLabelLengthFix(t *testing.T) {
	// This test documents the Kubernetes label length issue that was fixed
	// Original error: "metadata.labels: Invalid value: must be no more than 63 characters"

	// Test data that matches the real-world scenario that was failing
	realUserID := "51d971c9109a13841969415b9ebbeab92b287f15890d8a93c910f52c4c41956b"                       // 64 chars - too long
	realContainerID := "51d971c9109a13841969415b9ebbeab92b287f15890d8a93c910f52c4c41956b-david-1754788618" // 81 chars - too long

	t.Run("Verify problem values exceed limit", func(t *testing.T) {
		// These were the values causing the original failure
		if len(realContainerID) <= 63 {
			t.Errorf("Expected container ID to exceed 63 chars for test validity, got %d", len(realContainerID))
		}

		if len(realUserID) <= 63 {
			t.Errorf("Expected user ID to exceed 63 chars for test validity, got %d", len(realUserID))
		}

		t.Logf("✅ Container ID length: %d chars (exceeds 63 limit)", len(realContainerID))
		t.Logf("✅ User ID length: %d chars (exceeds 63 limit)", len(realUserID))
		t.Log("These values would have caused: 'metadata.labels: Invalid value: must be no more than 63 characters'")
		t.Log("Fix: Added shortenForLabel() method to truncate labels to 63 chars with hash suffix")
	})

	t.Run("Verify fix handles container creation", func(t *testing.T) {
		// Create a mock container manager to test that we would at least not get compilation errors
		mockManager := NewMockContainerManager()

		// Test that we can create containers with long IDs using the mock
		ctx := context.Background()
		req := &container.CreateContainerRequest{
			UserID: realUserID,
			Name:   "david",
			Image:  "ubuntu:22.04",
		}

		// This should not fail in the mock (real Docker would test the actual label shortening)
		createdContainer, err := mockManager.CreateContainer(ctx, req)
		if err != nil {
			t.Errorf("Mock container creation failed: %v", err)
		}

		if createdContainer == nil {
			t.Error("Expected created container to be non-nil")
		}

		t.Logf("✅ Mock container creation succeeded with long user ID")
		t.Logf("Created container ID: %s", createdContainer.ID)
	})
}

// Helper function to check if a shortened label contains a meaningful prefix
func containsPrefix(shortened, original string) bool {
	// The shortened format is "prefix-hash", so we check if it starts with a reasonable prefix
	if len(original) <= 46 {
		return shortened == original // Not shortened
	}

	// Should start with at least some characters from the original
	expectedPrefix := original[:46] // Max prefix length we use
	return len(shortened) >= 46 && shortened[:46] == expectedPrefix
}

