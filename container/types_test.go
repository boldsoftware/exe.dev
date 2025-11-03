package container

import (
	"testing"
)

func TestDefaultResources(t *testing.T) {
	if DefaultCPURequest != "1000m" {
		t.Errorf("DefaultCPURequest = %q, want %q", DefaultCPURequest, "1000m")
	}
	if DefaultMemoryRequest != "4Gi" {
		t.Errorf("DefaultMemoryRequest = %q, want %q", DefaultMemoryRequest, "4Gi")
	}
	if DefaultStorageSize != "20Gi" {
		t.Errorf("DefaultStorageSize = %q, want %q", DefaultStorageSize, "20Gi")
	}
}

func TestExpandImageName(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"exeuntu", "ghcr.io/boldsoftware/exeuntu:latest"},
		{"exeuntu:latest", "ghcr.io/boldsoftware/exeuntu:latest"},
		{"ubuntu", "docker.io/library/ubuntu:latest"},
		{"ubuntu:latest", "docker.io/library/ubuntu:latest"},
		{"debian", "ghcr.io/linuxcontainers/debian:bookworm"},
		{"alpine", "ghcr.io/linuxcontainers/alpine:latest"},
		{"python", "quay.io/sclorg/python-313"},
		{"node", "quay.io/sclorg/nodejs-22"},
		{"golang", "quay.io/sclorg/golang-1.25"},
		{"rust", "ghcr.io/rust-lang/rust:latest"},
		{"custom/image:tag", "custom/image:tag"},             // Should not be modified
		{"ghcr.io/user/repo:v1.0", "ghcr.io/user/repo:v1.0"}, // Should not be modified
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := ExpandImageName(tt.input)
			if result != tt.expected {
				t.Errorf("ExpandImageName(%q) = %q, expected %q", tt.input, result, tt.expected)
			}
		})
	}
}
