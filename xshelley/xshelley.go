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
	cacheDirErr  error

	// downloadMu serializes downloads per architecture to prevent concurrent
	// goroutines from racing on the same .tmp file.
	downloadMu sync.Map // goarch -> *sync.Mutex
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

// getCacheDir returns the cache directory, using a stable path under the user's
// cache directory. This avoids /tmp which gets cleaned by systemd-tmpfiles on
// long-running processes.
func getCacheDir() (string, error) {
	cacheDirOnce.Do(func() {
		dir, err := os.UserCacheDir()
		if err != nil {
			// Fall back to home dir
			home, err2 := os.UserHomeDir()
			if err2 != nil {
				cacheDirErr = fmt.Errorf("failed to find cache directory: %w", err)
				return
			}
			dir = filepath.Join(home, ".cache")
		}
		cacheDir = filepath.Join(dir, "xshelley")
	})
	if cacheDirErr != nil {
		return "", cacheDirErr
	}
	return cacheDir, nil
}

// GetShelley returns the path to the shelley binary for the specified architecture.
// It downloads the binary from the latest GitHub release if not cached, or if the
// cache is stale (older than refreshInterval).
//
// Parameters:
//   - ctx: context for cancellation and timeout
//   - goarch: target architecture (e.g., "amd64", "arm64")
//
// Only Linux binaries are available. Results are cached with hourly refresh checks.
func GetShelley(ctx context.Context, goarch string) (path string, err error) {
	platform := fmt.Sprintf("linux/%s", goarch)

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

	// Check if we have a valid cached version (fast path, no lock needed)
	needsRefresh, currentTag, err := shouldRefresh(metadataPath, platform)
	if err != nil {
		return "", err
	}

	if !needsRefresh {
		if _, err := os.Stat(shelleyBinaryPath); err == nil {
			return shelleyBinaryPath, nil
		}
	}

	// Serialize downloads per architecture to prevent concurrent goroutines
	// from racing on the same .tmp file.
	muI, _ := downloadMu.LoadOrStore(goarch, &sync.Mutex{})
	mu := muI.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock()

	// Re-check cache under the lock — another goroutine may have just finished.
	needsRefresh, currentTag, err = shouldRefresh(metadataPath, platform)
	if err != nil {
		return "", err
	}
	if !needsRefresh {
		if _, err := os.Stat(shelleyBinaryPath); err == nil {
			return shelleyBinaryPath, nil
		}
	}

	// Fetch the latest release info from GitHub
	release, err := fetchLatestRelease(ctx)
	if err != nil {
		return "", err
	}

	// If tag hasn't changed and binary exists, just update metadata
	if release.TagName == currentTag {
		if _, err := os.Stat(shelleyBinaryPath); err == nil {
			if err := updateMetadata(metadataPath, release.TagName, platform); err != nil {
				return "", err
			}
			return shelleyBinaryPath, nil
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
		return "", fmt.Errorf("no asset %q found in release %s", assetName, release.TagName)
	}

	// Download the binary
	if err := downloadFile(ctx, downloadURL, shelleyBinaryPath); err != nil {
		return "", fmt.Errorf("failed to download shelley: %w", err)
	}

	if err := updateMetadata(metadataPath, release.TagName, platform); err != nil {
		return "", err
	}

	return shelleyBinaryPath, nil
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

	// Ensure the destination directory exists. It may have been removed
	// (e.g., by systemd-tmpfiles) since it was originally created.
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
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
