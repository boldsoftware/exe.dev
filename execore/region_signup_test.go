package execore

import (
	"testing"

	"exe.dev/region"
)

// TestRegionForIPFallback verifies that regionForIP returns the default
// region when the IPQS API key is not configured.
func TestRegionForIPFallback(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)

	tests := []struct {
		name string
		ip   string
	}{
		{"empty IP", ""},
		{"loopback", "127.0.0.1"},
		{"public IP", "8.8.8.8"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := server.regionForIP(t.Context(), tt.ip)
			if r != region.Default() {
				t.Errorf("regionForIP(%q) = %v, want %v", tt.ip, r, region.Default())
			}
		})
	}
}
