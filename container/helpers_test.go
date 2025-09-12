package container

import (
	"testing"
)

func TestExpandImageNameForContainerd(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		// Simple names get docker.io/library/ prefix
		{"alpine", "docker.io/library/alpine:latest"},
		{"alpine:latest", "docker.io/library/alpine:latest"},
		{"alpine:3.18", "docker.io/library/alpine:3.18"},
		{"ubuntu", "docker.io/library/ubuntu:22.04"},
		{"ubuntu:latest", "docker.io/library/ubuntu:22.04"},
		{"ubuntu:20.04", "docker.io/library/ubuntu:20.04"},
		{"nginx", "docker.io/library/nginx:latest"},
		{"nginx:1.25", "docker.io/library/nginx:1.25"},

		// User images get docker.io/ prefix
		{"myuser/myimage", "docker.io/myuser/myimage:latest"},
		{"myuser/myimage:v1", "docker.io/myuser/myimage:v1"},

		// Full registry paths are not modified
		{"docker.io/library/alpine:latest", "docker.io/library/alpine:latest"},
		{"ghcr.io/user/repo:v1.0", "ghcr.io/user/repo:v1.0"},
		{"quay.io/user/image:latest", "quay.io/user/image:latest"},
		{"localhost:5000/myimage:latest", "localhost:5000/myimage:latest"},

		// Special case: exeuntu
		{"exeuntu", "ghcr.io/boldsoftware/exeuntu:latest"},
		{"exeuntu:latest", "ghcr.io/boldsoftware/exeuntu:latest"},

		// Special case: sha256
		{"sha256:decafbad", "sha256:decafbad"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := ExpandImageNameForContainerd(tt.input)
			if result != tt.expected {
				t.Errorf("ExpandImageNameForContainerd(%q) = %q, expected %q", tt.input, result, tt.expected)
			}
		})
	}
}
