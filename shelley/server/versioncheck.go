package server

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fynelabs/selfupdate"

	"shelley.exe.dev/version"
)

// VersionChecker checks for new versions of Shelley from GitHub releases.
type VersionChecker struct {
	mu          sync.Mutex
	lastCheck   time.Time
	cachedInfo  *VersionInfo
	skipCheck   bool
	githubOwner string
	githubRepo  string
}

// VersionInfo contains version check results.
type VersionInfo struct {
	CurrentVersion      string         `json:"current_version"`
	CurrentTag          string         `json:"current_tag,omitempty"`
	CurrentCommit       string         `json:"current_commit,omitempty"`
	CurrentCommitTime   string         `json:"current_commit_time,omitempty"`
	LatestVersion       string         `json:"latest_version,omitempty"`
	LatestTag           string         `json:"latest_tag,omitempty"`
	PublishedAt         time.Time      `json:"published_at,omitempty"`
	HasUpdate           bool           `json:"has_update"`    // True if minor version is newer (for showing upgrade button)
	ShouldNotify        bool           `json:"should_notify"` // True if should show red dot (newer + 5 days old)
	DownloadURL         string         `json:"download_url,omitempty"`
	ExecutablePath      string         `json:"executable_path,omitempty"`
	Commits             []CommitInfo   `json:"commits,omitempty"`
	CheckedAt           time.Time      `json:"checked_at"`
	Error               string         `json:"error,omitempty"`
	RunningUnderSystemd bool           `json:"running_under_systemd"` // True if INVOCATION_ID env var is set (systemd)
	ReleaseInfo         *GitHubRelease `json:"-"`                     // Internal, not exposed to JSON
}

// CommitInfo represents a commit in the changelog.
type CommitInfo struct {
	SHA     string    `json:"sha"`
	Message string    `json:"message"`
	Author  string    `json:"author"`
	Date    time.Time `json:"date"`
}

// GitHubRelease represents a GitHub release from the API.
type GitHubRelease struct {
	TagName     string    `json:"tag_name"`
	Name        string    `json:"name"`
	PublishedAt time.Time `json:"published_at"`
	Assets      []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

// GitHubCommit represents a commit from the GitHub API.
type GitHubCommit struct {
	SHA    string `json:"sha"`
	Commit struct {
		Message string `json:"message"`
		Author  struct {
			Name string    `json:"name"`
			Date time.Time `json:"date"`
		} `json:"author"`
	} `json:"commit"`
}

// NewVersionChecker creates a new version checker.
func NewVersionChecker() *VersionChecker {
	skipCheck := os.Getenv("SHELLEY_SKIP_VERSION_CHECK") == "true"
	return &VersionChecker{
		skipCheck:   skipCheck,
		githubOwner: "boldsoftware",
		githubRepo:  "shelley",
	}
}

// Check checks for a new version, using the cache if still valid.
func (vc *VersionChecker) Check(ctx context.Context, forceRefresh bool) (*VersionInfo, error) {
	if vc.skipCheck {
		info := version.GetInfo()
		return &VersionInfo{
			CurrentVersion:      info.Version,
			CurrentTag:          info.Tag,
			CurrentCommit:       info.Commit,
			HasUpdate:           false,
			CheckedAt:           time.Now(),
			RunningUnderSystemd: os.Getenv("INVOCATION_ID") != "",
		}, nil
	}

	vc.mu.Lock()
	defer vc.mu.Unlock()

	// Return cached info if still valid (6 hours) and not forcing refresh
	if !forceRefresh && vc.cachedInfo != nil && time.Since(vc.lastCheck) < 6*time.Hour {
		return vc.cachedInfo, nil
	}

	info, err := vc.fetchVersionInfo(ctx)
	if err != nil {
		// On error, return current version info with error
		currentInfo := version.GetInfo()
		return &VersionInfo{
			CurrentVersion:      currentInfo.Version,
			CurrentTag:          currentInfo.Tag,
			CurrentCommit:       currentInfo.Commit,
			HasUpdate:           false,
			CheckedAt:           time.Now(),
			Error:               err.Error(),
			RunningUnderSystemd: os.Getenv("INVOCATION_ID") != "",
		}, nil
	}

	vc.cachedInfo = info
	vc.lastCheck = time.Now()
	return info, nil
}

// fetchVersionInfo fetches the latest release info from GitHub.
func (vc *VersionChecker) fetchVersionInfo(ctx context.Context) (*VersionInfo, error) {
	currentInfo := version.GetInfo()
	execPath, _ := os.Executable()
	info := &VersionInfo{
		CurrentVersion:      currentInfo.Version,
		CurrentTag:          currentInfo.Tag,
		CurrentCommit:       currentInfo.Commit,
		CurrentCommitTime:   currentInfo.CommitTime,
		ExecutablePath:      execPath,
		CheckedAt:           time.Now(),
		RunningUnderSystemd: os.Getenv("INVOCATION_ID") != "",
	}

	// Fetch latest release
	latestRelease, err := vc.fetchLatestRelease(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch latest release: %w", err)
	}

	info.LatestTag = latestRelease.TagName
	info.LatestVersion = latestRelease.TagName
	info.PublishedAt = latestRelease.PublishedAt
	info.ReleaseInfo = latestRelease

	// Find the download URL for the current platform
	info.DownloadURL = vc.findDownloadURL(latestRelease)

	// Check if latest has a newer minor version
	info.HasUpdate = vc.isNewerMinor(currentInfo.Tag, latestRelease.TagName)

	// For ShouldNotify, we need to check if the versions are 5+ days apart
	// Fetch the current version's release to compare dates
	if info.HasUpdate && currentInfo.Tag != "" {
		currentRelease, err := vc.fetchRelease(ctx, currentInfo.Tag)
		if err == nil && currentRelease != nil {
			// Show notification if the latest release is 5+ days newer than current
			timeBetween := latestRelease.PublishedAt.Sub(currentRelease.PublishedAt)
			info.ShouldNotify = timeBetween >= 5*24*time.Hour
		} else {
			// Can't fetch current release info, just notify if there's an update
			info.ShouldNotify = true
		}
	}

	return info, nil
}

// FetchChangelog fetches the commits between current and latest versions.
func (vc *VersionChecker) FetchChangelog(ctx context.Context, currentTag, latestTag string) ([]CommitInfo, error) {
	if currentTag == "" || latestTag == "" {
		return nil, nil
	}

	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/compare/%s...%s",
		vc.githubOwner, vc.githubRepo, currentTag, latestTag)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("User-Agent", "Shelley-VersionChecker")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API returned status %d", resp.StatusCode)
	}

	var compareResp struct {
		Commits []GitHubCommit `json:"commits"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&compareResp); err != nil {
		return nil, err
	}

	var commits []CommitInfo
	for _, c := range compareResp.Commits {
		// Get first line of commit message
		message := c.Commit.Message
		if idx := indexOf(message, '\n'); idx != -1 {
			message = message[:idx]
		}
		commits = append(commits, CommitInfo{
			SHA:     c.SHA[:7],
			Message: message,
			Author:  c.Commit.Author.Name,
			Date:    c.Commit.Author.Date,
		})
	}

	// Sort commits by date, newest first
	sort.Slice(commits, func(i, j int) bool {
		return commits[i].Date.After(commits[j].Date)
	})

	return commits, nil
}

func indexOf(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}

// fetchRelease fetches a specific release by tag from GitHub.
func (vc *VersionChecker) fetchRelease(ctx context.Context, tag string) (*GitHubRelease, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/tags/%s",
		vc.githubOwner, vc.githubRepo, tag)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("User-Agent", "Shelley-VersionChecker")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API returned status %d", resp.StatusCode)
	}

	var release GitHubRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return nil, err
	}

	return &release, nil
}

// fetchLatestRelease fetches the latest release from GitHub.
func (vc *VersionChecker) fetchLatestRelease(ctx context.Context) (*GitHubRelease, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest",
		vc.githubOwner, vc.githubRepo)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("User-Agent", "Shelley-VersionChecker")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API returned status %d", resp.StatusCode)
	}

	var release GitHubRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return nil, err
	}

	return &release, nil
}

// findDownloadURL finds the appropriate download URL for the current platform.
func (vc *VersionChecker) findDownloadURL(release *GitHubRelease) string {
	// Build expected asset name: shelley_<os>_<arch>
	expectedName := fmt.Sprintf("shelley_%s_%s", runtime.GOOS, runtime.GOARCH)

	for _, asset := range release.Assets {
		if asset.Name == expectedName {
			return asset.BrowserDownloadURL
		}
	}

	return ""
}

// isNewerMinor checks if latest has a higher minor version than current.
func (vc *VersionChecker) isNewerMinor(currentTag, latestTag string) bool {
	currentMinor := parseMinorVersion(currentTag)
	latestMinor := parseMinorVersion(latestTag)
	return latestMinor > currentMinor
}

// parseMinorVersion extracts the X from v0.X.Y format.
func parseMinorVersion(tag string) int {
	if len(tag) < 2 || tag[0] != 'v' {
		return 0
	}

	// Skip 'v'
	s := tag[1:]

	// Find first dot
	firstDot := -1
	for i := 0; i < len(s); i++ {
		if s[i] == '.' {
			firstDot = i
			break
		}
	}
	if firstDot == -1 {
		return 0
	}

	// Skip major version and dot
	s = s[firstDot+1:]

	// Parse minor version
	var minor int
	for i := 0; i < len(s); i++ {
		if s[i] >= '0' && s[i] <= '9' {
			minor = minor*10 + int(s[i]-'0')
		} else {
			break
		}
	}

	return minor
}

// DoUpgrade downloads and applies the update with checksum verification.
func (vc *VersionChecker) DoUpgrade(ctx context.Context) error {
	if vc.skipCheck {
		return fmt.Errorf("version checking is disabled")
	}

	// Get cached info or fetch fresh
	info, err := vc.Check(ctx, false)
	if err != nil {
		return fmt.Errorf("failed to check version: %w", err)
	}

	if !info.HasUpdate {
		return fmt.Errorf("no update available")
	}

	if info.DownloadURL == "" {
		return fmt.Errorf("no download URL for %s/%s", runtime.GOOS, runtime.GOARCH)
	}

	if info.ReleaseInfo == nil {
		return fmt.Errorf("no release info available")
	}

	// Find and download checksums.txt
	expectedChecksum, err := vc.fetchExpectedChecksum(ctx, info.ReleaseInfo)
	if err != nil {
		return fmt.Errorf("failed to fetch checksum: %w", err)
	}

	// Download the binary
	resp, err := http.Get(info.DownloadURL)
	if err != nil {
		return fmt.Errorf("failed to download update: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download returned status %d", resp.StatusCode)
	}

	// Read the entire binary to verify checksum before applying
	binaryData, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read update: %w", err)
	}

	// Verify checksum
	actualChecksum := sha256.Sum256(binaryData)
	actualChecksumHex := hex.EncodeToString(actualChecksum[:])

	if actualChecksumHex != expectedChecksum {
		return fmt.Errorf("checksum mismatch: expected %s, got %s", expectedChecksum, actualChecksumHex)
	}

	// Apply the update
	if err := selfupdate.Apply(bytes.NewReader(binaryData), selfupdate.Options{}); err != nil {
		return fmt.Errorf("failed to apply update: %w", err)
	}

	return nil
}

// fetchExpectedChecksum downloads checksums.txt and extracts the expected checksum for our binary.
func (vc *VersionChecker) fetchExpectedChecksum(ctx context.Context, release *GitHubRelease) (string, error) {
	// Find checksums.txt URL
	var checksumURL string
	for _, asset := range release.Assets {
		if asset.Name == "checksums.txt" {
			checksumURL = asset.BrowserDownloadURL
			break
		}
	}
	if checksumURL == "" {
		return "", fmt.Errorf("checksums.txt not found in release")
	}

	// Download checksums.txt
	req, err := http.NewRequestWithContext(ctx, "GET", checksumURL, nil)
	if err != nil {
		return "", err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("failed to download checksums: status %d", resp.StatusCode)
	}

	// Parse checksums.txt (format: "checksum  filename")
	expectedBinaryName := fmt.Sprintf("shelley_%s_%s", runtime.GOOS, runtime.GOARCH)

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.Fields(line)
		if len(parts) >= 2 {
			checksum := parts[0]
			filename := parts[1]
			if filename == expectedBinaryName {
				return checksum, nil
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("error reading checksums: %w", err)
	}

	return "", fmt.Errorf("checksum for %s not found in checksums.txt", expectedBinaryName)
}
