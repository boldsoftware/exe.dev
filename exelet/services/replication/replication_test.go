package replication

import "testing"

func TestIsVMInstanceID(t *testing.T) {
	tests := []struct {
		id   string
		want bool
	}{
		{"vm000123-blue-falcon", true},
		{"vm999999-x", true},
		{"vm000000-a", true},
		{"vm00012-blue-falcon", false},   // only 5 digits
		{"vm0001234-blue-falcon", false}, // 7 digits (digit at position 8, not dash)
		{"data", false},
		{"tank", false},
		{"", false},
		{"vmabcdef-x", false}, // letters instead of digits
		{"VM000123-x", false}, // uppercase
		{"vm000123", false},   // no dash after digits
		{"xm000123-x", false}, // wrong first char
		{"vx000123-x", false}, // wrong second char
		{"v", false},          // too short
		{"vm", false},         // too short
		{"vm000123x", false},  // char at position 8 is not dash
	}

	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			got := isVMInstanceID(tt.id)
			if got != tt.want {
				t.Errorf("isVMInstanceID(%q) = %v, want %v", tt.id, got, tt.want)
			}
		})
	}
}

func TestRemoteVolumeID(t *testing.T) {
	tests := []struct {
		localID  string
		nodeName string
		want     string
	}{
		// VM instance IDs are unchanged
		{"vm000123-blue-falcon", "node1", "vm000123-blue-falcon"},
		{"vm999999-x", "node1", "vm999999-x"},

		// Non-VM datasets get node suffix
		{"data", "node1", "data-node1"},
		{"tank", "exelet-us-east-1", "tank-exelet-us-east-1"},

		// Already suffixed - don't double-suffix
		{"data-node1", "node1", "data-node1"},
		{"myvolume-exelet-us-east-1", "exelet-us-east-1", "myvolume-exelet-us-east-1"},

		// Partial suffix match should still add suffix
		{"data-node", "node1", "data-node-node1"},
	}

	for _, tt := range tests {
		t.Run(tt.localID+"/"+tt.nodeName, func(t *testing.T) {
			got := remoteVolumeID(tt.localID, tt.nodeName)
			if got != tt.want {
				t.Errorf("remoteVolumeID(%q, %q) = %q, want %q", tt.localID, tt.nodeName, got, tt.want)
			}
		})
	}
}
