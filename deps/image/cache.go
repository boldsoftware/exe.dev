package image

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"exe.dev/deps/image/types"
)

// MetadataCache provides file-based caching for image metadata.
// Cache keys include both the repository (registry + path) and digest to ensure:
// 1. Auth is enforced per-repository (no cross-repo cache hits)
// 2. Private manifest/config data isn't leaked across repositories
// 3. Cache only returns data for repositories that actually serve the digest
type MetadataCache struct {
	dataDir string
}

// NewMetadataCache creates a new metadata cache that stores data in the given directory.
func NewMetadataCache(dataDir string) *MetadataCache {
	return &MetadataCache{dataDir: dataDir}
}

// Get retrieves cached metadata for the given repository and digest.
// The repository should be the full image reference without tag/digest (e.g., "docker.io/library/nginx").
// Returns an error if the metadata is not cached.
func (c *MetadataCache) Get(repository, digest string) (*types.ImageMetadata, error) {
	path := c.metadataPath(repository, digest)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("metadata not cached for %s@%s: %w", repository, digest, err)
	}

	var metadata types.ImageMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return nil, fmt.Errorf("error unmarshaling cached metadata: %w", err)
	}

	return &metadata, nil
}

// Put stores metadata in the cache, keyed by repository and digest.
// The repository should be the full image reference without tag/digest (e.g., "docker.io/library/nginx").
func (c *MetadataCache) Put(repository, digest string, metadata *types.ImageMetadata) error {
	path := c.metadataPath(repository, digest)

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("error creating metadata cache directory: %w", err)
	}

	data, err := json.Marshal(metadata)
	if err != nil {
		return fmt.Errorf("error marshaling metadata: %w", err)
	}

	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("error writing cached metadata: %w", err)
	}

	return nil
}

// metadataPath returns the file path for cached metadata.
// Path structure: {dataDir}/metadata/{repoHash}/{algorithm}/{encodedDigest}.json
// The repository is hashed to create a safe directory name while maintaining separation.
func (c *MetadataCache) metadataPath(repository, digest string) string {
	// Hash the repository to create a safe, fixed-length directory name
	repoHash := hashRepository(repository)

	// Split "sha256:abc123..." into algorithm and encoded parts
	parts := strings.SplitN(digest, ":", 2)
	if len(parts) != 2 {
		// Fallback for malformed digests
		return filepath.Join(c.dataDir, "metadata", repoHash, "unknown", digest+".json")
	}
	algorithm := parts[0]
	encoded := parts[1]
	return filepath.Join(c.dataDir, "metadata", repoHash, algorithm, encoded+".json")
}

// hashRepository creates a deterministic hash of the repository name for use as a directory name.
func hashRepository(repository string) string {
	h := sha256.Sum256([]byte(repository))
	return hex.EncodeToString(h[:16]) // Use first 16 bytes (32 hex chars) for reasonable uniqueness
}
