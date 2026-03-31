package execore

import "testing"

func TestParseSize(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		input    string
		expected uint64
		wantErr  bool
	}{
		// Numbers without suffix default to GiB (binary: 1024^3)
		{"4", 4 * 1024 * 1024 * 1024, false},
		{"0", 0, false},
		{"20", 20 * 1024 * 1024 * 1024, false},

		// Bytes suffix
		{"1024B", 1024, false},
		{"1024b", 1024, false},

		// Kilobytes (binary: 1024)
		{"1K", 1024, false},
		{"1KB", 1024, false},
		{"1k", 1024, false},
		{"1kb", 1024, false},
		{"10K", 10240, false},

		// Megabytes (binary: 1024^2)
		{"1M", 1024 * 1024, false},
		{"1MB", 1024 * 1024, false},
		{"1m", 1024 * 1024, false},
		{"1mb", 1024 * 1024, false},
		{"100M", 100 * 1024 * 1024, false},
		{"1024MB", 1024 * 1024 * 1024, false},

		// Gigabytes (binary: 1024^3)
		{"1G", 1024 * 1024 * 1024, false},
		{"1GB", 1024 * 1024 * 1024, false},
		{"1g", 1024 * 1024 * 1024, false},
		{"1gb", 1024 * 1024 * 1024, false},
		{"2G", 2 * 1024 * 1024 * 1024, false},
		{"8GB", 8 * 1024 * 1024 * 1024, false},
		{"32GB", 32 * 1024 * 1024 * 1024, false},

		// GiB suffix (same as GB)
		{"1GiB", 1024 * 1024 * 1024, false},
		{"64GiB", 64 * 1024 * 1024 * 1024, false},

		// Terabytes (binary: 1024^4)
		{"1TB", 1024 * 1024 * 1024 * 1024, false},
		{"1TiB", 1024 * 1024 * 1024 * 1024, false},

		// With whitespace
		{" 1GB ", 1024 * 1024 * 1024, false},
		{"  2G  ", 2 * 1024 * 1024 * 1024, false},

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
