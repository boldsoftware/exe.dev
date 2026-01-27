package execore

import "testing"

func TestParseSize(t *testing.T) {
	testCases := []struct {
		input    string
		expected uint64
		wantErr  bool
	}{
		// Numbers without suffix default to GB (binary: 1024^3)
		{"4", 4 * 1024 * 1024 * 1024, false},
		{"0", 0, false},
		{"20", 20 * 1024 * 1024 * 1024, false},

		// Bytes suffix
		{"1024B", 1024, false},
		{"1024b", 1024, false},

		// Kilobytes (humanize uses SI units: 1000)
		{"1K", 1000, false},
		{"1KB", 1000, false},
		{"1k", 1000, false},
		{"1kb", 1000, false},
		{"10K", 10000, false},

		// Megabytes (humanize uses SI units: 1000^2)
		{"1M", 1000 * 1000, false},
		{"1MB", 1000 * 1000, false},
		{"1m", 1000 * 1000, false},
		{"1mb", 1000 * 1000, false},
		{"100M", 100 * 1000 * 1000, false},
		{"1024MB", 1024 * 1000 * 1000, false},

		// Gigabytes (humanize uses SI units: 1000^3)
		{"1G", 1000 * 1000 * 1000, false},
		{"1GB", 1000 * 1000 * 1000, false},
		{"1g", 1000 * 1000 * 1000, false},
		{"1gb", 1000 * 1000 * 1000, false},
		{"2G", 2 * 1000 * 1000 * 1000, false},
		{"8GB", 8 * 1000 * 1000 * 1000, false},
		{"32GB", 32 * 1000 * 1000 * 1000, false},

		// With whitespace
		{" 1GB ", 1000 * 1000 * 1000, false},
		{"  2G  ", 2 * 1000 * 1000 * 1000, false},

		// Error cases
		{"", 0, true},
		{"abc", 0, true},
		{"GB", 0, true}, // no number
	}

	for _, tc := range testCases {
		t.Run(tc.input, func(t *testing.T) {
			got, err := parseSize(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Errorf("parseSize(%q) = %d, want error", tc.input, got)
				}
				return
			}
			if err != nil {
				t.Errorf("parseSize(%q) error = %v, want nil", tc.input, err)
				return
			}
			if got != tc.expected {
				t.Errorf("parseSize(%q) = %d, want %d", tc.input, got, tc.expected)
			}
		})
	}
}
