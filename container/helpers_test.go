package container

import (
	"strings"
	"testing"
)

func TestValidateImageName(t *testing.T) {
	tests := []struct {
		name    string
		image   string
		wantErr bool
		errMsg  string
	}{
		// Valid images
		{"simple name", "alpine", false, ""},
		{"with tag", "alpine:latest", false, ""},
		{"with user", "myuser/myimage:v1", false, ""},
		{"full registry", "ghcr.io/boldsoftware/exeuntu:latest", false, ""},
		{"docker.io", "docker.io/library/ubuntu:22.04", false, ""},
		{"quay.io", "quay.io/sclorg/python-313", false, ""},
		{"with digest", "ubuntu@sha256:abc123def456", false, ""},

		// Empty - not allowed (caller should trim whitespace before calling)
		{"empty string", "", true, "cannot be empty"},

		// Localhost - not allowed
		{"localhost", "localhost/myimage", true, "is not valid"},
		{"localhost with port", "localhost:5000/myimage", true, "is not valid"},
		{"localhost uppercase", "LOCALHOST/myimage", true, "is not valid"},
		{"localhost mixed case", "LocalHost:5000/myimage", true, "is not valid"},
		{"127.0.0.1", "127.0.0.1/myimage", true, "is not valid"},
		{"127.0.0.1 with port", "127.0.0.1:5000/myimage", true, "is not valid"},

		// URL schemes - not allowed
		{"file scheme", "file:///path/to/image", true, "is not valid"},
		{"http scheme", "http://registry.example.com/image", true, "is not valid"},
		{"https scheme", "https://registry.example.com/image", true, "is not valid"},
		{"oci scheme", "oci:///some/path", true, "is not valid"},

		// Absolute paths - not allowed
		{"absolute path", "/var/lib/containers/image", true, "is not valid"},
		{"absolute path with tag", "/path/to/image:latest", true, "is not valid"},

		// Relative paths - not allowed
		{"relative path current", "./local/image", true, "is not valid"},
		{"relative path parent", "../other/image", true, "is not valid"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateImageName(tt.image)
			if tt.wantErr {
				if err == nil {
					t.Errorf("ValidateImageName(%q) = nil, expected error containing %q", tt.image, tt.errMsg)
				} else if tt.errMsg != "" && !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("ValidateImageName(%q) = %q, expected error containing %q", tt.image, err.Error(), tt.errMsg)
				}
			} else {
				if err != nil {
					t.Errorf("ValidateImageName(%q) = %q, expected nil", tt.image, err.Error())
				}
			}
		})
	}
}

func TestExpandImageNameForContainerd(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		// Simple names expand to the configured registries
		{"alpine", "ghcr.io/linuxcontainers/alpine:latest"},
		{"alpine:latest", "ghcr.io/linuxcontainers/alpine:latest"},
		{"alpine:3.18", "docker.io/library/alpine:3.18"},
		{"ubuntu", "docker.io/library/ubuntu:latest"},
		{"ubuntu:latest", "docker.io/library/ubuntu:latest"},
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

func TestGetDisplayImageName(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"alpine", "alpine"},
		{"alpine:latest", "alpine"},
		{"alpine:3.18", "alpine:3.18"},
		{"ubuntu", "ubuntu"},
		{"ubuntu:latest", "ubuntu"},
		{"ubuntu:20.04", "ubuntu:20.04"},
		{"nginx", "nginx"},
		{"nginx:1.25", "nginx:1.25"},

		{"myuser/myimage", "myuser/myimage"},
		{"myuser/myimage:v1", "myuser/myimage:v1"},

		{"docker.io/library/alpine:latest", "library/alpine"},
		{"ghcr.io/user/repo:v1.0", "user/repo:v1.0"},
		{"quay.io/user/image:latest", "user/image"},
		{"localhost:5000/myimage:latest", "myimage"},

		{"exeuntu", "exeuntu"},
		{"ghcr.io/boldsoftware/exeuntu:latest", "boldsoftware/exeuntu"},
		{"ghcr.io/boldsoftware/exeuntu", "boldsoftware/exeuntu"},

		{"sha256:3ae37701a5351fd8d06946b020b7cc7f6527ffa2715c8e8393968dc6fe62d861", "local:3ae37701"},
		{"boldsoftware/exeuntu@sha256:3ae37701a5351fd8d06946b020b7cc7f6527ffa2715c8e8393968dc6fe62d861", "boldsoftware/exeuntu@sha256:3ae37701"},
		{"ghcr.io/boldsoftware/exeuntu@sha256:3ae37701a5351fd8d06946b020b7cc7f6527ffa2715c8e8393968dc6fe62d861", "boldsoftware/exeuntu@sha256:3ae37701"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := GetDisplayImageName(tt.input)
			if result != tt.expected {
				t.Errorf("ExpandImageNameForContainerd(%q) = %q, expected %q", tt.input, result, tt.expected)
			}
		})
	}
}
