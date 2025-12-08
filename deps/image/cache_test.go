package image

import (
	"os"
	"path/filepath"
	"testing"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"exe.dev/deps/image/types"
)

func TestMetadataCache_PutAndGet(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "cache-test-")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cache := NewMetadataCache(tmpDir)

	repository := "docker.io/library/nginx"
	digest := "sha256:abc123def456"
	metadata := &types.ImageMetadata{
		Digest:      digest,
		MediaType:   ocispec.MediaTypeImageManifest,
		Size:        1234,
		ContentSize: 5678,
		Config: &ocispec.Image{
			Platform: ocispec.Platform{
				Architecture: "amd64",
				OS:           "linux",
			},
		},
	}

	// Put metadata
	if err := cache.Put(repository, digest, metadata); err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	// Get metadata back
	retrieved, err := cache.Get(repository, digest)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}

	// Verify retrieved metadata matches
	if retrieved.Digest != metadata.Digest {
		t.Errorf("Digest mismatch: got %s, want %s", retrieved.Digest, metadata.Digest)
	}
	if retrieved.MediaType != metadata.MediaType {
		t.Errorf("MediaType mismatch: got %s, want %s", retrieved.MediaType, metadata.MediaType)
	}
	if retrieved.Size != metadata.Size {
		t.Errorf("Size mismatch: got %d, want %d", retrieved.Size, metadata.Size)
	}
	if retrieved.ContentSize != metadata.ContentSize {
		t.Errorf("ContentSize mismatch: got %d, want %d", retrieved.ContentSize, metadata.ContentSize)
	}
	if retrieved.Config == nil {
		t.Fatal("Config is nil")
	}
	if retrieved.Config.Platform.Architecture != metadata.Config.Platform.Architecture {
		t.Errorf("Config.Architecture mismatch: got %s, want %s", retrieved.Config.Platform.Architecture, metadata.Config.Platform.Architecture)
	}
	if retrieved.Config.Platform.OS != metadata.Config.Platform.OS {
		t.Errorf("Config.OS mismatch: got %s, want %s", retrieved.Config.Platform.OS, metadata.Config.Platform.OS)
	}
}

func TestMetadataCache_GetNonExistent(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "cache-test-")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cache := NewMetadataCache(tmpDir)

	_, err = cache.Get("docker.io/library/nginx", "sha256:nonexistent")
	if err == nil {
		t.Error("expected error for non-existent digest, got nil")
	}
}

func TestMetadataCache_OverwriteExisting(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "cache-test-")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cache := NewMetadataCache(tmpDir)

	repository := "docker.io/library/nginx"
	digest := "sha256:abc123"
	metadata1 := &types.ImageMetadata{
		Digest: digest,
		Size:   100,
	}
	metadata2 := &types.ImageMetadata{
		Digest: digest,
		Size:   200,
	}

	// Put first version
	if err := cache.Put(repository, digest, metadata1); err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	// Overwrite with second version
	if err := cache.Put(repository, digest, metadata2); err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	// Get should return second version
	retrieved, err := cache.Get(repository, digest)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if retrieved.Size != 200 {
		t.Errorf("Size mismatch: got %d, want 200", retrieved.Size)
	}
}

func TestMetadataCache_CrossRepoIsolation(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "cache-test-")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cache := NewMetadataCache(tmpDir)

	digest := "sha256:abc123"
	repo1 := "docker.io/library/nginx"
	repo2 := "docker.io/private/secret-image"

	metadata1 := &types.ImageMetadata{
		Digest: digest,
		Size:   100,
	}

	// Put metadata for repo1
	if err := cache.Put(repo1, digest, metadata1); err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	// Get from repo1 should succeed
	if _, err := cache.Get(repo1, digest); err != nil {
		t.Errorf("Get from repo1 should succeed: %v", err)
	}

	// Get from repo2 with same digest should fail (cross-repo isolation)
	if _, err := cache.Get(repo2, digest); err == nil {
		t.Error("Get from repo2 should fail - same digest but different repository")
	}
}

func TestMetadataCache_MetadataPath(t *testing.T) {
	cache := NewMetadataCache(filepath.Join("data"))

	// Get the expected repo hash for a known repository
	repoHash := hashRepository("docker.io/library/nginx")

	tests := []struct {
		repository string
		digest     string
		expected   string
	}{
		{
			"docker.io/library/nginx",
			"sha256:abc123",
			filepath.Join("data", "metadata", repoHash, "sha256", "abc123.json"),
		},
		{
			"docker.io/library/nginx",
			"sha512:def456",
			filepath.Join("data", "metadata", repoHash, "sha512", "def456.json"),
		},
		{
			"docker.io/library/nginx",
			"malformed",
			filepath.Join("data", "metadata", repoHash, "unknown", "malformed.json"),
		},
	}

	for _, tt := range tests {
		got := cache.metadataPath(tt.repository, tt.digest)
		if got != tt.expected {
			t.Errorf("metadataPath(%q, %q) = %q, want %q", tt.repository, tt.digest, got, tt.expected)
		}
	}
}

func TestMetadataCache_DifferentReposDifferentPaths(t *testing.T) {
	cache := NewMetadataCache(filepath.Join("data"))

	repo1 := "docker.io/library/nginx"
	repo2 := "gcr.io/my-project/my-image"
	digest := "sha256:abc123"

	path1 := cache.metadataPath(repo1, digest)
	path2 := cache.metadataPath(repo2, digest)

	if path1 == path2 {
		t.Errorf("different repositories should have different cache paths, but both got %q", path1)
	}
}
