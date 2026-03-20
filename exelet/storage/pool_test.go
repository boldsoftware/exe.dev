package storage

import "testing"

func TestPoolNameFromAddress(t *testing.T) {
	tests := []struct {
		addr string
		want string
	}{
		{"zfs:///var/tmp/exelet/storage?dataset=tank", "tank"},
		{"zfs:///var/tmp/exelet/storage?dataset=nvme", "nvme"},
		{"zfs:///var/tmp/exelet/storage?dataset=tank/child", "tank"},
		{"zfs:///var/tmp/exelet/storage", ""},
		{"", ""},
		{"invalid://url", ""},
	}
	for _, tt := range tests {
		got := PoolNameFromAddress(tt.addr)
		if got != tt.want {
			t.Errorf("PoolNameFromAddress(%q) = %q, want %q", tt.addr, got, tt.want)
		}
	}
}
