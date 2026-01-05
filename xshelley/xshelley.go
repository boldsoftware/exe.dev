// Package xshelley provides a simple interface to download the shelley binary
// from the published ghcr.io/boldsoftware/exeuntu multi-arch Docker image.
package xshelley

import (
	"archive/tar"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

const (
	defaultImage     = "ghcr.io/boldsoftware/exeuntu:latest"
	shelleyPath      = "usr/local/bin/shelley"
	refreshInterval  = 1 * time.Hour
	metadataFileName = "metadata.json"
	shelleyFileName  = "shelley"
)

var (
	cacheDirOnce sync.Once
	cacheDir     string
)

type cacheMetadata struct {
	ImageDigest string    `json:"image_digest"`
	LastChecked time.Time `json:"last_checked"`
	Platform    string    `json:"platform"`
}

// getCacheDir returns the cache directory, initializing it once with a secure random path
func getCacheDir() (string, error) {
	var initErr error
	cacheDirOnce.Do(func() {
		// Create a secure temporary cache directory with random name
		var err error
		cacheDir, err = os.MkdirTemp("", "xshelley-cache-")
		if err != nil {
			initErr = fmt.Errorf("failed to create cache directory: %w", err)
		}
	})
	if initErr != nil {
		return "", initErr
	}
	if cacheDir == "" {
		return "", fmt.Errorf("cache directory not initialized")
	}
	return cacheDir, nil
}

// GetShelley returns the path to the shelley binary for the specified architecture.
// It downloads the binary from the Docker image if not cached, or if the cache
// is stale (older than refreshInterval).
//
// The binary is extracted from the ghcr.io/boldsoftware/exeuntu image by:
// 1. Fetching the image manifest for the specified architecture
// 2. Identifying which layer contains /usr/local/bin/shelley
// 3. Downloading only that layer
// 4. Extracting the shelley binary
//
// Parameters:
//   - ctx: context for cancellation and timeout
//   - goarch: target architecture (e.g., "amd64", "arm64")
//
// Only Linux binaries are available. Results are cached in /tmp/xshelley-cache
// with hourly refresh checks.
func GetShelley(ctx context.Context, goarch string) (string, error) {
	platform := fmt.Sprintf("linux/%s", goarch)

	// Get cache directory (created once with secure random name)
	cacheDirPath, err := getCacheDir()
	if err != nil {
		return "", err
	}

	platformDir := filepath.Join(cacheDirPath, fmt.Sprintf("linux-%s", goarch))
	if err := os.MkdirAll(platformDir, 0o755); err != nil {
		return "", fmt.Errorf("failed to create platform cache directory: %w", err)
	}

	metadataPath := filepath.Join(platformDir, metadataFileName)
	shelleyBinaryPath := filepath.Join(platformDir, shelleyFileName)

	// Check if we have a valid cached version
	needsRefresh, currentDigest, err := shouldRefresh(ctx, metadataPath, platform)
	if err != nil {
		return "", err
	}

	if !needsRefresh {
		// Verify the binary still exists
		if _, err := os.Stat(shelleyBinaryPath); err == nil {
			return shelleyBinaryPath, nil
		}
	}

	// Need to download
	ref, err := name.ParseReference(defaultImage)
	if err != nil {
		return "", fmt.Errorf("failed to parse image reference: %w", err)
	}

	// Get the image for the specified platform (Linux only)
	img, err := remote.Image(ref,
		remote.WithContext(ctx),
		remote.WithAuthFromKeychain(authn.DefaultKeychain),
		remote.WithPlatform(v1.Platform{
			OS:           "linux",
			Architecture: goarch,
		}),
	)
	if err != nil {
		return "", fmt.Errorf("failed to fetch image: %w", err)
	}

	// Get the image digest
	digest, err := img.Digest()
	if err != nil {
		return "", fmt.Errorf("failed to get image digest: %w", err)
	}

	// If digest hasn't changed and binary exists, just update metadata
	if digest.String() == currentDigest {
		if _, err := os.Stat(shelleyBinaryPath); err == nil {
			if err := updateMetadata(metadataPath, digest.String(), platform); err != nil {
				return "", err
			}
			return shelleyBinaryPath, nil
		}
	}

	// Find and extract the shelley binary
	if err := extractShelley(img, shelleyBinaryPath); err != nil {
		return "", err
	}

	// Update metadata
	if err := updateMetadata(metadataPath, digest.String(), platform); err != nil {
		return "", err
	}

	return shelleyBinaryPath, nil
}

func shouldRefresh(ctx context.Context, metadataPath, platform string) (bool, string, error) {
	data, err := os.ReadFile(metadataPath)
	if err != nil {
		if os.IsNotExist(err) {
			return true, "", nil
		}
		return false, "", fmt.Errorf("failed to read metadata: %w", err)
	}

	var meta cacheMetadata
	if err := json.Unmarshal(data, &meta); err != nil {
		return true, "", nil // Invalid metadata, refresh
	}

	// Check if it's time to refresh
	if time.Since(meta.LastChecked) < refreshInterval {
		return false, meta.ImageDigest, nil
	}

	return true, meta.ImageDigest, nil
}

func updateMetadata(metadataPath, digest, platform string) error {
	meta := cacheMetadata{
		ImageDigest: digest,
		LastChecked: time.Now(),
		Platform:    platform,
	}

	data, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("failed to marshal metadata: %w", err)
	}

	if err := os.WriteFile(metadataPath, data, 0o644); err != nil {
		return fmt.Errorf("failed to write metadata: %w", err)
	}

	return nil
}

func extractShelley(img v1.Image, outputPath string) error {
	// Get the config to find which layer has the shelley binary
	configFile, err := img.ConfigFile()
	if err != nil {
		return fmt.Errorf("failed to get config file: %w", err)
	}

	layers, err := img.Layers()
	if err != nil {
		return fmt.Errorf("failed to get image layers: %w", err)
	}

	// Find the layer that contains the shelley binary
	// Look through the history to find the COPY command that adds shelley
	targetLayerIdx := -1
	for i, history := range configFile.History {
		if history.EmptyLayer {
			continue
		}

		// Look for the COPY command that adds shelley to /usr/local/bin
		if strings.Contains(history.CreatedBy, "shelley") &&
			strings.Contains(history.CreatedBy, "/usr/local/bin/shelley") {
			targetLayerIdx = i
			break
		}
	}

	if targetLayerIdx == -1 {
		return fmt.Errorf("could not find layer containing shelley binary")
	}

	// Adjust for non-empty layers only
	actualLayerIdx := 0
	for i := 0; i <= targetLayerIdx; i++ {
		if i == targetLayerIdx {
			break
		}
		if !configFile.History[i].EmptyLayer {
			actualLayerIdx++
		}
	}

	// Extract shelley from the target layer
	layer := layers[actualLayerIdx]
	rc, err := layer.Uncompressed()
	if err != nil {
		return fmt.Errorf("failed to get layer contents: %w", err)
	}
	defer rc.Close()

	tr := tar.NewReader(rc)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("failed to read tar: %w", err)
		}

		// Look for the shelley binary
		if strings.HasSuffix(header.Name, shelleyPath) || header.Name == shelleyPath {
			// Extract to output path
			out, err := os.OpenFile(outputPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
			if err != nil {
				return fmt.Errorf("failed to create output file: %w", err)
			}
			defer out.Close()

			if _, err := io.Copy(out, tr); err != nil {
				return fmt.Errorf("failed to write binary: %w", err)
			}

			return nil
		}
	}

	return fmt.Errorf("shelley binary not found in layer")
}
