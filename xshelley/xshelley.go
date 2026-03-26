// Package xshelley provides a simple interface to download the shelley binary
// from GitHub releases at github.com/boldsoftware/shelley.
package xshelley

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"tailscale.com/util/singleflight"
)

const (
	refreshInterval  = 1 * time.Hour
	metadataFileName = "metadata.json"
	shelleyFileName  = "shelley"

	// maxRetries must be > 0; retryableGet returns (nil, nil) if 0.
	maxRetries = 3

	// singleflightTimeout bounds the total lifetime of a singleflight
	// download worker, including retries and Retry-After waits for both
	// the release-fetch and binary-download phases.
	singleflightTimeout = 10 * time.Minute
)

var (
	latestReleaseURL = "https://api.github.com/repos/boldsoftware/shelley/releases/latest"
	retryBaseWait    = 1 * time.Second

	cacheDirOnce sync.Once
	cacheDir     string
	cacheDirErr  error

	sfGroup = &singleflight.Group[string, string]{}

	// httpTransport clones DefaultTransport to preserve proxy support,
	// keep-alive, and idle-connection management, then overrides
	// phase-specific timeouts so stuck phases fail fast.
	httpTransport = func() *http.Transport {
		t := http.DefaultTransport.(*http.Transport).Clone()
		t.DialContext = (&net.Dialer{Timeout: 10 * time.Second}).DialContext
		t.TLSHandshakeTimeout = 5 * time.Second
		t.ResponseHeaderTimeout = 10 * time.Second
		return t
	}()

	// httpClient pairs the transport with a generous overall deadline
	// as a safety net for stalled body reads (server alive but not
	// sending data). The shelley binary is a few MB; 5 minutes is
	// never the bottleneck for a real download.
	httpClient = &http.Client{
		Transport: httpTransport,
		Timeout:   5 * time.Minute,
	}
)

type cacheMetadata struct {
	ReleaseTag  string    `json:"release_tag"`
	LastChecked time.Time `json:"last_checked"`
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
				// Final fallback for headless environments (e.g. systemd
				// services) where neither $XDG_CACHE_HOME nor $HOME is set.
				dir = "/var/cache"
			} else {
				dir = filepath.Join(home, ".cache")
			}
		}
		cacheDir = filepath.Join(dir, "xshelley")
	})
	if cacheDirErr != nil {
		return "", cacheDirErr
	}
	return cacheDir, nil
}

// ShelleyInfo contains the path and version of a cached shelley binary.
type ShelleyInfo struct {
	Path    string // filesystem path to the binary
	Version string // release tag (e.g. "v0.89.914374232")
}

// GetShelleyInfo returns the path and version of the shelley binary for the
// specified architecture. It behaves like GetShelley but also reads the cached
// release tag so callers can log or compare versions.
func GetShelleyInfo(ctx context.Context, goarch string) (ShelleyInfo, error) {
	path, err := GetShelley(ctx, goarch)
	if err != nil {
		return ShelleyInfo{}, err
	}

	cacheDirPath, err := getCacheDir()
	if err != nil {
		return ShelleyInfo{Path: path}, nil
	}
	metadataPath := filepath.Join(cacheDirPath, fmt.Sprintf("linux-%s", goarch), metadataFileName)
	data, err := os.ReadFile(metadataPath)
	if err != nil {
		return ShelleyInfo{Path: path}, nil
	}
	var meta cacheMetadata
	if err := json.Unmarshal(data, &meta); err != nil {
		return ShelleyInfo{Path: path}, nil
	}
	return ShelleyInfo{Path: path, Version: meta.ReleaseTag}, nil
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

	// Sample jitter once so this goroutine's fast-path and its
	// singleflight closure agree on staleness if it becomes the
	// executing goroutine.
	interval := jitteredRefreshInterval()

	// Fast path: valid cache and binary on disk.
	needsRefresh, _, err := shouldRefresh(metadataPath, interval)
	if err != nil {
		return "", err
	}
	if !needsRefresh {
		if _, err := os.Stat(shelleyBinaryPath); err == nil {
			return shelleyBinaryPath, nil
		}
	}

	// Slow path: fetch from GitHub. Singleflight collapses N concurrent
	// callers into one download. We use DoChan (not DoChanContext) with a
	// background context so the download runs to completion even if all
	// callers bail — the result is cached on disk, so finishing a
	// partially-complete download benefits the next wave of callers.
	// singleflightTimeout bounds the overall goroutine lifetime.
	ch := sfGroup.DoChan(goarch, func() (string, error) {
		// Re-check shouldRefresh inside the closure so currentTag is
		// derived by the executing goroutine, not captured from a caller.
		_, currentTag, err := shouldRefresh(metadataPath, interval)
		if err != nil {
			return "", err
		}

		// Use a background context with an overall deadline: once started,
		// finish the download regardless of whether individual callers are
		// still waiting, but don't let a stuck worker pin the singleflight
		// key indefinitely.
		dlCtx, dlCancel := context.WithTimeout(context.Background(), singleflightTimeout)
		defer dlCancel()

		release, err := fetchLatestRelease(dlCtx)
		if err != nil {
			return "", err
		}

		// Tag unchanged and binary present: just refresh the timestamp.
		if release.TagName == currentTag {
			if _, err := os.Stat(shelleyBinaryPath); err == nil {
				if err := updateMetadata(metadataPath, release.TagName); err != nil {
					return "", err
				}
				return shelleyBinaryPath, nil
			}
		}

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

		if err := downloadFile(dlCtx, downloadURL, shelleyBinaryPath); err != nil {
			return "", fmt.Errorf("failed to download shelley: %w", err)
		}

		if err := updateMetadata(metadataPath, release.TagName); err != nil {
			return "", err
		}

		return shelleyBinaryPath, nil
	})

	// Wait for either the shared result or our caller's cancellation.
	select {
	case res := <-ch:
		if res.Err != nil {
			return "", res.Err
		}
		return res.Val, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// retryableGet performs an HTTP GET with per-request timeouts and retries on
// transient failures (5xx, 429, and 403-with-Retry-After status codes and
// network errors). For 429 responses, the Retry-After header is respected.
func retryableGet(ctx context.Context, url string, header http.Header) (*http.Response, error) {
	client := httpClient
	var lastErr error
	var retryAfter time.Duration
	for attempt := range maxRetries {
		if attempt > 0 {
			wait := max(retryBaseWait*time.Duration(1<<(attempt-1)), retryAfter) // exponential: 1s, 2s
			retryAfter = 0
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(wait):
			}
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		for k, vs := range header {
			for _, v := range vs {
				req.Header.Add(k, v)
			}
		}

		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 ||
			(resp.StatusCode == http.StatusForbidden && resp.Header.Get("Retry-After") != "") {
			resp.Body.Close()
			lastErr = fmt.Errorf("HTTP %d from %s", resp.StatusCode, url)
			// GitHub sends Retry-After as integer seconds (confirmed by docs
			// and go-github). The HTTP spec also allows HTTP-date format, but
			// GitHub doesn't use it; we intentionally don't parse it.
			if ra := resp.Header.Get("Retry-After"); ra != "" {
				if secs, err := strconv.Atoi(ra); err == nil && secs > 0 {
					retryAfter = time.Duration(secs) * time.Second
				}
			}
			continue
		}
		return resp, nil
	}
	return nil, lastErr
}

func fetchLatestRelease(ctx context.Context) (*ghRelease, error) {
	hdr := http.Header{"Accept": {"application/vnd.github+json"}}
	resp, err := retryableGet(ctx, latestReleaseURL, hdr)
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
	resp, err := retryableGet(ctx, url, nil)
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

	// The temp path is deterministic per architecture. This is safe because
	// singleflight deduplicates concurrent downloads within this process;
	// if multiple processes download the same architecture, they race here.
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

// jitteredRefreshInterval returns refreshInterval +/-10% to prevent
// fleet-wide cache expiry synchronization.
func jitteredRefreshInterval() time.Duration {
	jitter := refreshInterval / 10
	return refreshInterval - jitter + time.Duration(rand.Int64N(int64(2*jitter)))
}

func shouldRefresh(metadataPath string, interval time.Duration) (bool, string, error) {
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

	if time.Since(meta.LastChecked) < interval {
		return false, meta.ReleaseTag, nil
	}

	return true, meta.ReleaseTag, nil
}

func updateMetadata(metadataPath, tag string) error {
	meta := cacheMetadata{
		ReleaseTag:  tag,
		LastChecked: time.Now(),
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
