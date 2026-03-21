package storage

import (
	"reflect"
	"testing"
)

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

func TestMetadataFromAddress(t *testing.T) {
	tests := []struct {
		name string
		addr string
		want map[string]string
	}{
		{
			name: "metadata params extracted",
			addr: "zfs:///data/exelet/storage?dataset=dozer&type=nvme&tier=hot",
			want: map[string]string{"type": "nvme", "tier": "hot"},
		},
		{
			name: "dataset-only returns nil",
			addr: "zfs:///data/exelet/storage?dataset=tank",
			want: nil,
		},
		{
			name: "no query params returns nil",
			addr: "zfs:///data/exelet/storage",
			want: nil,
		},
		{
			name: "empty string returns nil",
			addr: "",
			want: nil,
		},
		{
			name: "single metadata param",
			addr: "zfs:///data/exelet/storage?dataset=tank&tier=cold",
			want: map[string]string{"tier": "cold"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MetadataFromAddress(tt.addr)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("MetadataFromAddress(%q) = %v, want %v", tt.addr, got, tt.want)
			}
		})
	}
}
