package tagresolver

import (
	"context"
	"testing"
	"time"
)

func TestHostUpdater(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	tr := New(db)

	// Create host updater with mock hosts
	hosts := []string{"ssh://test-host-1", "ssh://test-host-2"}
	hu := NewHostUpdater(db, tr, hosts)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start the host updater
	hu.Start(ctx)

	// Send a tag update
	update := TagUpdate{
		Registry:       "docker.io",
		Repository:     "library/nginx",
		Tag:            "latest",
		Platform:       "linux/amd64",
		PlatformDigest: "sha256:nginx789",
	}

	// Send update through the channel
	select {
	case tr.updateChan <- update:
		// Sent successfully
	case <-time.After(1 * time.Second):
		t.Fatal("Failed to send update")
	}

	// Give it a moment to process
	time.Sleep(100 * time.Millisecond)

	// Stop the updater
	hu.Stop()
}

func TestPullInFlight(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	tr := New(db)
	hosts := []string{"ssh://test-host"}
	hu := NewHostUpdater(db, tr, hosts)

	// Test that duplicate pulls are prevented
	digest := "sha256:test123"

	// First pull should succeed in marking as in-flight
	hu.mu.Lock()
	if hu.pullInFlight[digest] {
		t.Error("Digest already marked as in-flight")
	}
	hu.pullInFlight[digest] = true
	hu.mu.Unlock()

	// Second attempt should see it's already in-flight
	hu.mu.RLock()
	inFlight := hu.pullInFlight[digest]
	hu.mu.RUnlock()

	if !inFlight {
		t.Error("Digest not marked as in-flight")
	}

	// Clean up
	hu.mu.Lock()
	delete(hu.pullInFlight, digest)
	hu.mu.Unlock()

	// Verify it's cleaned up
	hu.mu.RLock()
	inFlight = hu.pullInFlight[digest]
	hu.mu.RUnlock()

	if inFlight {
		t.Error("Digest still marked as in-flight after cleanup")
	}
}

func TestShouldPrefetch(t *testing.T) {
	tests := []struct {
		imageRef string
		want     bool
	}{
		{"ubuntu@sha256:abc", true},
		{"debian:latest", true},
		{"alpine:3.18", true},
		{"python:3.11", true},
		{"node:20", true},
		{"golang:1.21", true},
		{"rust:latest", true},
		{"ghcr.io/boldsoftware/exeuntu:latest", true},
		{"random/image:v1", false},
		{"mycompany/app:v2.0.1", false},
	}

	for _, tt := range tests {
		t.Run(tt.imageRef, func(t *testing.T) {
			got := shouldPrefetch(tt.imageRef)
			if got != tt.want {
				t.Errorf("shouldPrefetch(%q) = %v, want %v", tt.imageRef, got, tt.want)
			}
		})
	}
}

func TestNormalizeDockerHubReferences(t *testing.T) {
	tests := []struct {
		registry   string
		repository string
		digest     string
		want       string
	}{
		{
			registry:   "docker.io",
			repository: "library/ubuntu",
			digest:     "sha256:abc123",
			want:       "ubuntu@sha256:abc123",
		},
		{
			registry:   "docker.io",
			repository: "myuser/myimage",
			digest:     "sha256:def456",
			want:       "myuser/myimage@sha256:def456",
		},
		{
			registry:   "ghcr.io",
			repository: "owner/repo",
			digest:     "sha256:ghi789",
			want:       "ghcr.io/owner/repo@sha256:ghi789",
		},
	}

	for _, tt := range tests {
		t.Run(tt.repository, func(t *testing.T) {
			// This logic is in handleTagUpdate
			imageRef := formatImageRef(tt.registry, tt.repository, tt.digest)
			if imageRef != tt.want {
				t.Errorf("formatImageRef(%q, %q, %q) = %q, want %q",
					tt.registry, tt.repository, tt.digest, imageRef, tt.want)
			}
		})
	}
}

// Helper function to format image references (extracted from handleTagUpdate logic)
func formatImageRef(registry, repository, digest string) string {
	imageRef := registry + "/" + repository + "@" + digest

	// Normalize Docker Hub references
	if registry == "docker.io" {
		if len(repository) > 8 && repository[:8] == "library/" {
			// Official images don't need library/ prefix
			imageRef = repository[8:] + "@" + digest
		} else {
			imageRef = repository + "@" + digest
		}
	}

	return imageRef
}
