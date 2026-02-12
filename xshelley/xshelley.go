// Package xshelley provides a simple interface to download the shelley binary
// from GitHub releases at github.com/boldsoftware/shelley.
package xshelley

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	latestReleaseURL = "https://api.github.com/repos/boldsoftware/shelley/releases/latest"
	refreshInterval  = 1 * time.Hour
	metadataFileName = "metadata.json"
	shelleyFileName  = "shelley"
)

var (
	cacheDirOnce sync.Once
	cacheDir     string
)

type cacheMetadata struct {
	ReleaseTag  string    `json:"release_tag"`
	LastChecked time.Time `json:"last_checked"`
	Platform    string    `json:"platform"`
}

type ghRelease struct {
	TagName string    `json:"tag_name"`
	Assets  []ghAsset `json:"assets"`
}

type ghAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

// getCacheDir returns the cache directory, initializing it once with a secure random path
func getCacheDir() (string, error) {
	var initErr error
	cacheDirOnce.Do(func() {
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

// GetShelley returns the path to the shelley binary for the specified architecture
// and whether the result was served from cache.
// It downloads the binary from the latest GitHub release if not cached, or if the
// cache is stale (older than refreshInterval).
//
// Parameters:
//   - ctx: context for cancellation and timeout
//   - goarch: target architecture (e.g., "amd64", "arm64")
//
// Only Linux binaries are available. Results are cached with hourly refresh checks.
func GetShelley(ctx context.Context, goarch string) (path string, cached bool, err error) {
	platform := fmt.Sprintf("linux/%s", goarch)

	cacheDirPath, err := getCacheDir()
	if err != nil {
		return "", false, err
	}

	platformDir := filepath.Join(cacheDirPath, fmt.Sprintf("linux-%s", goarch))
	if err := os.MkdirAll(platformDir, 0o755); err != nil {
		return "", false, fmt.Errorf("failed to create platform cache directory: %w", err)
	}

	metadataPath := filepath.Join(platformDir, metadataFileName)
	shelleyBinaryPath := filepath.Join(platformDir, shelleyFileName)

	// Check if we have a valid cached version
	needsRefresh, currentTag, err := shouldRefresh(metadataPath, platform)
	if err != nil {
		return "", false, err
	}

	if !needsRefresh {
		if _, err := os.Stat(shelleyBinaryPath); err == nil {
			return shelleyBinaryPath, true, nil
		}
	}

	// Fetch the latest release info from GitHub
	release, err := fetchLatestRelease(ctx)
	if err != nil {
		return "", false, err
	}

	// If tag hasn't changed and binary exists, just update metadata
	if release.TagName == currentTag {
		if _, err := os.Stat(shelleyBinaryPath); err == nil {
			if err := updateMetadata(metadataPath, release.TagName, platform); err != nil {
				return "", false, err
			}
			return shelleyBinaryPath, true, nil
		}
	}

	// Find the asset for this architecture
	assetName := fmt.Sprintf("shelley_linux_%s", goarch)
	var downloadURL string
	for _, asset := range release.Assets {
		if asset.Name == assetName {
			downloadURL = asset.BrowserDownloadURL
			break
		}
	}
	if downloadURL == "" {
		return "", false, fmt.Errorf("no asset %q found in release %s", assetName, release.TagName)
	}

	// Download the binary
	if err := downloadFile(ctx, downloadURL, shelleyBinaryPath); err != nil {
		return "", false, fmt.Errorf("failed to download shelley: %w", err)
	}

	if err := updateMetadata(metadataPath, release.TagName, platform); err != nil {
		return "", false, err
	}

	return shelleyBinaryPath, false, nil
}

func fetchLatestRelease(ctx context.Context) (*ghRelease, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, latestReleaseURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch latest release: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API returned status %d", resp.StatusCode)
	}

	var release ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return nil, fmt.Errorf("failed to decode release response: %w", err)
	}
	return &release, nil
}

func downloadFile(ctx context.Context, url, dest string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download returned status %d", resp.StatusCode)
	}

	tmp := dest + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}

	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}

	return os.Rename(tmp, dest)
}

func shouldRefresh(metadataPath, platform string) (bool, string, error) {
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

	if time.Since(meta.LastChecked) < refreshInterval {
		return false, meta.ReleaseTag, nil
	}

	return true, meta.ReleaseTag, nil
}

func updateMetadata(metadataPath, tag, platform string) error {
	meta := cacheMetadata{
		ReleaseTag:  tag,
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
