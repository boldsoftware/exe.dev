package container

import (
	"strings"
	"testing"
)

func TestShortenForLabel(t *testing.T) {
	manager := &GKEManager{}

	testCases := []struct {
		name          string
		input         string
		expectShort   bool
		maxLength     int
	}{
		{
			name:          "short string unchanged",
			input:         "short",
			expectShort:   false,
			maxLength:     63,
		},
		{
			name:          "exact 63 chars unchanged",
			input:         strings.Repeat("a", 63),
			expectShort:   false,
			maxLength:     63,
		},
		{
			name:          "long container ID shortened",
			input:         "51d971c9109a13841969415b9ebbeab92b287f15890d8a93c910f52c4c41956b-david-1754788618",
			expectShort:   true,
			maxLength:     63,
		},
		{
			name:          "long user ID shortened",
			input:         "51d971c9109a13841969415b9ebbeab92b287f15890d8a93c910f52c4c41956b",
			expectShort:   true,
			maxLength:     63,
		},
		{
			name:          "very long string shortened",
			input:         strings.Repeat("x", 200),
			expectShort:   true,
			maxLength:     63,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := manager.shortenForLabel(tc.input)

			// Check length constraint
			if len(result) > tc.maxLength {
				t.Errorf("Expected result length <= %d, got %d", tc.maxLength, len(result))
			}

			// Check if shortening was applied correctly
			if tc.expectShort {
				if result == tc.input {
					t.Error("Expected input to be shortened, but it wasn't")
				}
				// Should contain a hash (16 chars) at the end
				parts := strings.Split(result, "-")
				lastPart := parts[len(parts)-1]
				if len(lastPart) != 16 {
					t.Errorf("Expected last part to be 16-char hash, got %d chars: %s", len(lastPart), lastPart)
				}
				// Should start with a prefix from original
				if !strings.HasPrefix(result, tc.input[:min(len(tc.input), 46)]) {
					t.Error("Expected result to start with prefix from original input")
				}
			} else {
				if result != tc.input {
					t.Errorf("Expected input to remain unchanged, got %s", result)
				}
			}

			t.Logf("Input: %s (len=%d)", tc.input, len(tc.input))
			t.Logf("Output: %s (len=%d)", result, len(result))
		})
	}
}

func TestRealWorldLabelValues(t *testing.T) {
	manager := &GKEManager{}

	// Test with real values that caused the original issue
	realContainerID := "51d971c9109a13841969415b9ebbeab92b287f15890d8a93c910f52c4c41956b-david-1754788618"
	realUserID := "51d971c9109a13841969415b9ebbeab92b287f15890d8a93c910f52c4c41956b"

	containerLabel := manager.shortenForLabel(realContainerID)
	userLabel := manager.shortenForLabel(realUserID)

	// Both should be within limits
	if len(containerLabel) > 63 {
		t.Errorf("Container label too long: %d chars", len(containerLabel))
	}
	if len(userLabel) > 63 {
		t.Errorf("User label too long: %d chars", len(userLabel))
	}

	// Both should be different (unless by extreme coincidence)
	if containerLabel == userLabel {
		t.Error("Container and user labels should be different")
	}

	t.Logf("Container ID: %s -> %s (%d chars)", realContainerID, containerLabel, len(containerLabel))
	t.Logf("User ID: %s -> %s (%d chars)", realUserID, userLabel, len(userLabel))
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}