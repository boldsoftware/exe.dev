package exe

import (
	"testing"
)

func TestGeneratedBoxNamesAreValid(t *testing.T) {
	t.Parallel()
	server := NewTestServer(t)

	// Test that generateRandomContainerName creates valid names
	for i := 0; i < 10; i++ {
		name := generateRandomContainerName()

		if !server.isValidBoxName(name) {
			t.Errorf("Generated name '%s' is not valid", name)
		}

		// Check length
		if len(name) > 30 {
			t.Errorf("Generated name '%s' is too long (%d chars)", name, len(name))
		}
	}
}
