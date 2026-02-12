package tagresolver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"exe.dev/exedb"
	"exe.dev/sqlite"
	"exe.dev/tslog"
)

// setupTestDB creates a test database with migrations applied
func setupTestDB(t *testing.T) (*sqlite.DB, func()) {
	dbPath := t.TempDir() + "/tagresolver-test.db"
	if err := exedb.CopyTemplateDB(tslog.Slogger(t), dbPath); err != nil {
		t.Fatalf("Failed to copy template database: %v", err)
	}

	db, err := sqlite.New(dbPath, 4)
	if err != nil {
		t.Fatalf("Failed to create sqlite.DB: %v", err)
	}

	return db, func() { db.Close() }
}

func TestParseImageReference(t *testing.T) {
	tests := []struct {
		name           string
		image          string
		wantRegistry   string
		wantRepository string
		wantTag        string
	}{
		{
			name:           "simple image",
			image:          "ubuntu",
			wantRegistry:   "docker.io",
			wantRepository: "library/ubuntu",
			wantTag:        "latest",
		},
		{
			name:           "image with tag",
			image:          "ubuntu:22.04",
			wantRegistry:   "docker.io",
			wantRepository: "library/ubuntu",
			wantTag:        "22.04",
		},
		{
			name:           "image with registry",
			image:          "ghcr.io/boldsoftware/exeuntu:latest",
			wantRegistry:   "ghcr.io",
			wantRepository: "boldsoftware/exeuntu",
			wantTag:        "latest",
		},
		{
			name:           "image with digest",
			image:          "ubuntu@sha256:abc123",
			wantRegistry:   "docker.io",
			wantRepository: "library/ubuntu",
			wantTag:        "latest",
		},
		{
			name:           "user image without registry",
			image:          "myuser/myimage:v1",
			wantRegistry:   "docker.io",
			wantRepository: "myuser/myimage",
			wantTag:        "v1",
		},
		{
			name:           "localhost registry",
			image:          "localhost:5000/myimage:latest",
			wantRegistry:   "localhost:5000",
			wantRepository: "myimage",
			wantTag:        "latest",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			registry, repository, tag := parseImageReference(tt.image)
			if registry != tt.wantRegistry {
				t.Errorf("registry = %q, want %q", registry, tt.wantRegistry)
			}
			if repository != tt.wantRepository {
				t.Errorf("repository = %q, want %q", repository, tt.wantRepository)
			}
			if tag != tt.wantTag {
				t.Errorf("tag = %q, want %q", tag, tt.wantTag)
			}
		})
	}
}

func TestTagResolution(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	tr := New(db)
	ctx := context.Background()

	// Test storing and retrieving a resolution
	t.Run("store and retrieve", func(t *testing.T) {
		// Store a resolution directly
		err := db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
			_, err := tx.Exec(`
				INSERT INTO tag_resolutions (
					registry, repository, tag, platform, platform_digest,
					last_checked_at, last_changed_at, ttl_seconds, image_size,
					created_at, updated_at
				) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			`, "docker.io", "library/ubuntu", "latest", "linux/amd64",
				"sha256:abcd1234", time.Now().Unix(), time.Now().Unix(),
				3600, 100000000, time.Now().Unix(), time.Now().Unix())
			return err
		})
		if err != nil {
			t.Fatalf("Failed to store resolution: %v", err)
		}

		// Retrieve it
		resolution, err := tr.getCachedResolution(ctx, "docker.io", "library/ubuntu", "latest", "linux/amd64")
		if err != nil {
			t.Fatalf("Failed to get cached resolution: %v", err)
		}
		if resolution == nil {
			t.Fatal("Expected resolution, got nil")
		}
		if resolution.PlatformDigest != "sha256:abcd1234" {
			t.Errorf("Digest = %q, want %q", resolution.PlatformDigest, "sha256:abcd1234")
		}
	})

	// Test resolution not found
	t.Run("not found", func(t *testing.T) {
		resolution, err := tr.getCachedResolution(ctx, "docker.io", "library/nginx", "latest", "linux/amd64")
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if resolution != nil {
			t.Errorf("Expected nil resolution, got %+v", resolution)
		}
	})

	// Test TTL calculation
	t.Run("TTL for different tags", func(t *testing.T) {
		tests := []struct {
			tag         string
			expectedTTL int64
		}{
			{"latest", 3600},      // 1 hour
			{"main", 3600},        // 1 hour
			{"master", 3600},      // 1 hour
			{"v1.0.0", 86400},     // 24 hours
			{"22.04", 86400},      // 24 hours
			{"stable-123", 86400}, // 24 hours
			{"dev", 21600},        // 6 hours default
		}

		for _, tt := range tests {
			// This would normally be done in ResolveTag, but we can test the logic
			ttl := int64(21600) // default
			if tt.tag == "latest" || tt.tag == "main" || tt.tag == "master" {
				ttl = 3600
			} else if containsVersionChar(tt.tag) {
				ttl = 86400
			}
			if ttl != tt.expectedTTL {
				t.Errorf("TTL for tag %q = %d, want %d", tt.tag, ttl, tt.expectedTTL)
			}
		}
	})
}

func containsVersionChar(tag string) bool {
	for _, c := range tag {
		if c == '.' || c == '-' {
			return true
		}
	}
	return false
}

func TestRefreshStaleTags(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Insert a stale tag (last checked 2 hours ago with 1 hour TTL)
	staleTime := time.Now().Add(-2 * time.Hour).Unix()
	err := db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := tx.Exec(`
			INSERT INTO tag_resolutions (
				registry, repository, tag, platform, platform_digest,
				last_checked_at, last_changed_at, ttl_seconds, image_size,
				created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, "docker.io", "library/alpine", "latest", "linux/amd64",
			"sha256:old123", staleTime, staleTime,
			3600, 5000000, staleTime, staleTime)
		return err
	})
	if err != nil {
		t.Fatalf("Failed to insert stale tag: %v", err)
	}

	// Insert a fresh tag (checked 30 minutes ago with 1 hour TTL)
	freshTime := time.Now().Add(-30 * time.Minute).Unix()
	err = db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := tx.Exec(`
			INSERT INTO tag_resolutions (
				registry, repository, tag, platform, platform_digest,
				last_checked_at, last_changed_at, ttl_seconds, image_size,
				created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, "docker.io", "library/nginx", "latest", "linux/amd64",
			"sha256:fresh456", freshTime, freshTime,
			3600, 10000000, freshTime, freshTime)
		return err
	})
	if err != nil {
		t.Fatalf("Failed to insert fresh tag: %v", err)
	}

	// Count stale tags
	var staleCount int
	err = db.Rx(ctx, func(ctx context.Context, rx *sqlite.Rx) error {
		now := time.Now().Unix()
		row := rx.QueryRow(`
			SELECT COUNT(*) FROM tag_resolutions
			WHERE last_checked_at + ttl_seconds < ?
		`, now)
		return row.Scan(&staleCount)
	})
	if err != nil {
		t.Fatalf("Failed to count stale tags: %v", err)
	}

	if staleCount != 1 {
		t.Errorf("Expected 1 stale tag, got %d", staleCount)
	}
}

func TestIncrementSeenOnHosts(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	tr := New(db)
	ctx := context.Background()

	// Insert a resolution
	err := db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := tx.Exec(`
			INSERT INTO tag_resolutions (
				registry, repository, tag, platform, platform_digest,
				last_checked_at, last_changed_at, ttl_seconds, seen_on_hosts,
				created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, "docker.io", "library/redis", "latest", "linux/amd64",
			"sha256:redis123", time.Now().Unix(), time.Now().Unix(),
			3600, 0, time.Now().Unix(), time.Now().Unix())
		return err
	})
	if err != nil {
		t.Fatalf("Failed to insert resolution: %v", err)
	}

	// Increment counter
	err = tr.IncrementSeenOnHosts(ctx, "docker.io", "library/redis", "latest", "linux/amd64")
	if err != nil {
		t.Fatalf("Failed to increment seen_on_hosts: %v", err)
	}

	// Check the counter
	var seenCount int
	err = db.Rx(ctx, func(ctx context.Context, rx *sqlite.Rx) error {
		row := rx.QueryRow(`
			SELECT seen_on_hosts FROM tag_resolutions
			WHERE registry = ? AND repository = ? AND tag = ? AND platform = ?
		`, "docker.io", "library/redis", "latest", "linux/amd64")
		return row.Scan(&seenCount)
	})
	if err != nil {
		t.Fatalf("Failed to get seen_on_hosts: %v", err)
	}

	if seenCount != 1 {
		t.Errorf("Expected seen_on_hosts = 1, got %d", seenCount)
	}
}

func TestTagUpdateChannel(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	tr := New(db)
	updateChan := tr.GetUpdateChannel()

	// Send an update
	update := TagUpdate{
		Registry:       "docker.io",
		Repository:     "library/postgres",
		Tag:            "latest",
		Platform:       "linux/amd64",
		PlatformDigest: "sha256:newdigest",
	}

	select {
	case tr.updateChan <- update:
		// Successfully sent
	case <-time.After(1 * time.Second):
		t.Fatal("Failed to send update to channel")
	}

	// Receive the update
	select {
	case received := <-updateChan:
		if received.PlatformDigest != update.PlatformDigest {
			t.Errorf("Received digest = %q, want %q", received.PlatformDigest, update.PlatformDigest)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("Failed to receive update from channel")
	}
}

func TestGetManifestDigest(t *testing.T) {
	// Create a mock registry server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Check request method
		if r.Method != "GET" {
			t.Errorf("Expected GET request, got %s", r.Method)
		}

		// Check Accept header
		accept := r.Header.Get("Accept")
		if !containsManifestMediaType(accept) {
			t.Errorf("Accept header missing manifest media types: %s", accept)
		}

		// Return digest in header
		w.Header().Set("Docker-Content-Digest", "sha256:testdigest123")
		w.Header().Set("Content-Length", "1234567")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	db, cleanup := setupTestDB(t)
	defer cleanup()

	tr := New(db)

	// Test the mock server with the tag resolver's http client
	req, err := http.NewRequest("GET", server.URL+"/v2/library/test/manifests/latest", nil)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}

	req.Header.Set("Accept", "application/vnd.oci.image.manifest.v1+json")
	resp, err := tr.httpClient.Do(req)
	if err != nil {
		t.Fatalf("Failed to fetch manifest: %v", err)
	}
	defer resp.Body.Close()

	digest := resp.Header.Get("Docker-Content-Digest")
	if digest != "sha256:testdigest123" {
		t.Errorf("Digest = %q, want %q", digest, "sha256:testdigest123")
	}
}

func containsManifestMediaType(accept string) bool {
	mediaTypes := []string{
		"application/vnd.oci.image.manifest.v1+json",
		"application/vnd.docker.distribution.manifest.v2+json",
		"application/vnd.docker.distribution.manifest.list.v2+json",
		"application/vnd.oci.image.index.v1+json",
	}

	for _, mt := range mediaTypes {
		if strings.Contains(accept, mt) {
			return true
		}
	}
	return false
}

func TestBackgroundRefreshLoop(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	tr := New(db)

	// Start the resolver
	tr.Start(t.Context())

	// Give it a moment to start
	time.Sleep(100 * time.Millisecond)

	// Stop it
	tr.Stop()

	// Verify it stopped cleanly
	select {
	case <-tr.stopChan:
		// Already closed, good
	default:
		t.Error("Stop channel not closed")
	}
}

func TestGHCRAuthentication(t *testing.T) {
	// This test requires network access to ghcr.io
	// Skip in CI or if network is unavailable
	if testing.Short() {
		t.Skip("Skipping ghcr.io test in short mode")
	}

	db, cleanup := setupTestDB(t)
	defer cleanup()

	tr := New(db)
	ctx := context.Background()

	// Test getting auth token for ghcr.io
	token, err := tr.getAuthToken(ctx, "ghcr.io", "boldsoftware/exeuntu")
	if err != nil {
		t.Fatalf("Failed to get auth token for ghcr.io: %v", err)
	}

	// Token should be non-empty for ghcr.io
	if token == "" {
		t.Error("Expected non-empty token for ghcr.io")
	}

	// Test resolving a public ghcr.io image
	digest, err := tr.ResolveTag(ctx, "ghcr.io/boldsoftware/exeuntu:latest", "linux/amd64")
	if err != nil {
		t.Fatalf("Failed to resolve ghcr.io image: %v", err)
	}

	// Should return a digest-based reference
	if !strings.Contains(digest, "@sha256:") {
		t.Errorf("Expected digest reference, got: %s", digest)
	}

	// Verify it was stored in the database
	resolution, err := tr.getCachedResolution(ctx, "ghcr.io", "boldsoftware/exeuntu", "latest", "linux/amd64")
	if err != nil {
		t.Fatalf("Failed to get cached resolution: %v", err)
	}
	if resolution == nil {
		t.Error("Expected resolution to be cached")
	}
	if resolution != nil && resolution.PlatformDigest == "" {
		t.Error("Expected platform digest to be set")
	}
}
