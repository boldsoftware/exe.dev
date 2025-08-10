package container

import (
	"testing"
)

func TestExtractContainerNameFromID(t *testing.T) {
	tests := []struct {
		name        string
		containerID string
		expected    string
	}{
		{
			name:        "simple name",
			containerID: "51d971c9109a13841969415b9ebbeab92b287f15890d8a93c910f52c4c41956b-david-1754788618",
			expected:    "david",
		},
		{
			name:        "name with dashes",
			containerID: "51d971c9109a13841969415b9ebbeab92b287f15890d8a93c910f52c4c41956b-my-awesome-container-1754788618",
			expected:    "my-awesome-container",
		},
		{
			name:        "single character name",
			containerID: "51d971c9109a13841969415b9ebbeab92b287f15890d8a93c910f52c4c41956b-a-1754788618",
			expected:    "a",
		},
		{
			name:        "name with numbers",
			containerID: "51d971c9109a13841969415b9ebbeab92b287f15890d8a93c910f52c4c41956b-test123-1754788618",
			expected:    "test123",
		},
		{
			name:        "malformed id - fallback",
			containerID: "invalid-format",
			expected:    "invalid-format", // Falls back to original if can't parse properly
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractContainerNameFromID(tt.containerID)
			if result != tt.expected {
				t.Errorf("extractContainerNameFromID(%q) = %q, want %q", tt.containerID, result, tt.expected)
			}
		})
	}
}