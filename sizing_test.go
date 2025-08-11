package exe

import (
	"testing"
)

func TestIsValidStorageSize(t *testing.T) {
	tests := []struct {
		input    string
		expected bool
	}{
		{"10Gi", true},
		{"100Gi", true},
		{"5Mi", true},
		{"1Ti", true},
		{"500Ki", true},
		{"1024Gi", true},
		{"", false},
		{"10", false},
		{"Gi", false},
		{"10GB", false}, // Wrong unit
		{"10gi", false}, // Wrong case
		{"abc", false},
		{"10 Gi", false}, // Space not allowed
		{"-10Gi", false}, // Negative not allowed
	}
	
	for _, test := range tests {
		result := isValidStorageSize(test.input)
		if result != test.expected {
			t.Errorf("isValidStorageSize(%q) = %v, expected %v", test.input, result, test.expected)
		}
	}
}