package utils

import "testing"

func TestMakeDigestRef(t *testing.T) {
	tests := []struct {
		name     string
		imageRef string
		digest   string
		want     string
	}{
		{
			name:     "simple tag",
			imageRef: "docker.io/library/ubuntu:latest",
			digest:   "sha256:abc123",
			want:     "docker.io/library/ubuntu@sha256:abc123",
		},
		{
			name:     "no tag",
			imageRef: "docker.io/library/ubuntu",
			digest:   "sha256:abc123",
			want:     "docker.io/library/ubuntu@sha256:abc123",
		},
		{
			name:     "existing digest",
			imageRef: "docker.io/library/ubuntu@sha256:old",
			digest:   "sha256:abc123",
			want:     "docker.io/library/ubuntu@sha256:abc123",
		},
		{
			name:     "registry with port",
			imageRef: "localhost:5000/myimage:v1",
			digest:   "sha256:abc123",
			want:     "localhost:5000/myimage@sha256:abc123",
		},
		{
			name:     "registry with port no tag",
			imageRef: "localhost:5000/myimage",
			digest:   "sha256:abc123",
			want:     "localhost:5000/myimage@sha256:abc123",
		},
		{
			name:     "nested path with tag",
			imageRef: "gcr.io/my-project/subdir/image:v2.0",
			digest:   "sha256:abc123",
			want:     "gcr.io/my-project/subdir/image@sha256:abc123",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := makeDigestRef(tt.imageRef, tt.digest)
			if got != tt.want {
				t.Errorf("makeDigestRef(%q, %q) = %q, want %q", tt.imageRef, tt.digest, got, tt.want)
			}
		})
	}
}
