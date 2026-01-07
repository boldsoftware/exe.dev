package server

import (
	"testing"
)

func TestEtagMatches(t *testing.T) {
	tests := []struct {
		name        string
		ifNoneMatch string
		etag        string
		want        bool
	}{
		// Basic matching
		{"exact match", `"abc123"`, `"abc123"`, true},
		{"no match", `"abc123"`, `"xyz789"`, false},
		{"empty if-none-match", "", `"abc123"`, false},

		// Weak validators (W/ prefix)
		{"weak validator match", `W/"abc123"`, `"abc123"`, true},
		{"weak etag match", `"abc123"`, `W/"abc123"`, true},
		{"both weak match", `W/"abc123"`, `W/"abc123"`, true},
		{"weak no match", `W/"abc123"`, `"xyz789"`, false},

		// Multiple ETags
		{"multiple first", `"abc123", "def456"`, `"abc123"`, true},
		{"multiple second", `"abc123", "def456"`, `"def456"`, true},
		{"multiple none", `"abc123", "def456"`, `"xyz789"`, false},
		{"multiple with spaces", `"a" , "b" , "c"`, `"b"`, true},
		{"multiple with weak", `"a", W/"b", "c"`, `"b"`, true},

		// Wildcard
		{"wildcard", "*", `"anything"`, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := etagMatches(tt.ifNoneMatch, tt.etag)
			if got != tt.want {
				t.Errorf("etagMatches(%q, %q) = %v, want %v", tt.ifNoneMatch, tt.etag, got, tt.want)
			}
		})
	}
}
