package compute

import (
	"fmt"
	"testing"
)

func TestExtractBaseImageID(t *testing.T) {
	tests := []struct {
		origin string
		want   string
	}{
		{"", ""},
		{"tank/sha256:abc123@snap", "sha256:abc123"},
		{"tank/e1e-XXXX/sha256:abc123@snap", "sha256:abc123"},
		{"tank/a/b/sha256:abc123@snap", "sha256:abc123"},
		{"sha256:abc123@snap", "sha256:abc123"},
		{"tank/sha256:abc123", "sha256:abc123"},
		{"tank/instance-id@migration", "instance-id"},
	}
	for _, tt := range tests {
		got := extractBaseImageID(tt.origin)
		if got != tt.want {
			t.Errorf("extractBaseImageID(%q) = %q, want %q", tt.origin, got, tt.want)
		}
	}
}

func TestIsStaleResumeTokenErr(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"generic error", fmt.Errorf("something broke"), false},
		{"connection reset", fmt.Errorf("write tcp: connection reset by peer"), false},
		{"cannot resume send", fmt.Errorf("zfs send -t: zfs send failed: exit status 255 (cannot resume send: 'tank/vm@snap' used in the initial send)"), true},
		{"no longer same snapshot", fmt.Errorf("zfs send -t: zfs send failed: exit status 255 (cannot resume send: 'tank/vm@migration-pre' is no longer the same snapshot used in the initial send\n)"), true},
		{"wrapped", fmt.Errorf("sideband: %w", fmt.Errorf("cannot resume send: stale")), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isStaleResumeTokenErr(tt.err)
			if got != tt.want {
				t.Errorf("isStaleResumeTokenErr(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
