// Package tagresolver implements a tag resolver for container images.
//
// It tracks image tag to digest mappings, periodically checks for updates to mutable
// tags (like :latest), and ensures all container hosts have fresh images pre-pulled.
//
// The resolver separates "tag resolution" from "container start" to maintain fast
// startup times (~1s) while keeping images reasonably fresh. Tags are resolved to
// digests in the background, and hosts are notified to pre-pull updated images.
package tagresolver

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"exe.dev/exedb"
	"exe.dev/sqlite"
)

// ImageConfig represents the configuration of a container image
type ImageConfig struct {
	Entrypoint   []string
	Cmd          []string
	User         string
	Labels       map[string]string
	ExposedPorts map[string]struct{}
}

// TagResolver manages image tag to digest resolution
type TagResolver struct {
	db         *sqlite.DB
	httpClient *http.Client

	// Channel for notifying about tag updates
	updateChan chan TagUpdate

	// Control channels
	stopChan chan struct{}
	wg       sync.WaitGroup
}

// TagUpdate represents an update to a tag's digest
type TagUpdate struct {
	Registry       string
	Repository     string
	Tag            string
	Platform       string
	IndexDigest    string
	PlatformDigest string
}

// TagResolution represents a tag to digest mapping
type TagResolution struct {
	Registry       string
	Repository     string
	Tag            string
	IndexDigest    string
	PlatformDigest string
	Platform       string
	LastCheckedAt  time.Time
	LastChangedAt  time.Time
	TTL            time.Duration
	SeenOnHosts    int
	ImageSize      int64
}

// RegistryAuth holds authentication details for a registry
type RegistryAuth struct {
	Username string
	Password string
	Token    string
}

// New creates a new TagResolver
func New(db *sqlite.DB) *TagResolver {
	return &TagResolver{
		db: db,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{
					InsecureSkipVerify: false,
				},
			},
		},
		updateChan: make(chan TagUpdate, 100),
		stopChan:   make(chan struct{}),
	}
}

// Start begins the background refresh loop
func (tr *TagResolver) Start(ctx context.Context) {
	tr.wg.Add(1)
	go tr.refreshLoop(ctx)
}

// Stop stops the background refresh loop
func (tr *TagResolver) Stop() {
	close(tr.stopChan)
	tr.wg.Wait()
}

// GetUpdateChannel returns the channel for tag updates
func (tr *TagResolver) GetUpdateChannel() <-chan TagUpdate {
	return tr.updateChan
}

// refreshLoop periodically checks for stale tags and refreshes them
func (tr *TagResolver) refreshLoop(ctx context.Context) {
	defer tr.wg.Done()

	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	// Initial refresh on startup
	tr.refreshStaleTags(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-tr.stopChan:
			return
		case <-ticker.C:
			tr.refreshStaleTags(ctx)
		}
	}
}

// refreshStaleTags finds and refreshes tags that are past their TTL
func (tr *TagResolver) refreshStaleTags(ctx context.Context) {
	now := time.Now().Unix()

	var toRefresh []TagResolution

	// Find tags that need refreshing
	err := exedb.WithRx(tr.db, ctx, func(ctx context.Context, queries *exedb.Queries) error {
		results, err := queries.GetTagsNeedingRefresh(ctx, exedb.GetTagsNeedingRefreshParams{
			LastCheckedAt: now,
			Limit:         10,
		})
		if err != nil {
			return err
		}

		for _, result := range results {
			tr := TagResolution{
				Registry:       result.Registry,
				Repository:     result.Repository,
				Tag:            result.Tag,
				Platform:       result.Platform,
				IndexDigest:    result.IndexDigest,
				PlatformDigest: result.PlatformDigest,
				LastCheckedAt:  time.Unix(result.LastCheckedAt, 0),
				TTL:            time.Duration(result.TtlSeconds) * time.Second,
			}
			toRefresh = append(toRefresh, tr)
		}
		return nil
	})
	if err != nil {
		log.Printf("Failed to query stale tags: %v", err)
		return
	}

	// Refresh each tag
	for _, tag := range toRefresh {
		if err := tr.refreshTag(ctx, tag); err != nil {
			log.Printf("Failed to refresh tag %s/%s:%s: %v",
				tag.Registry, tag.Repository, tag.Tag, err)
		}

		// Small delay between refreshes to avoid hammering registries
		select {
		case <-time.After(100 * time.Millisecond):
		case <-ctx.Done():
			return
		}
	}
}

// refreshTag checks upstream for updates to a specific tag
func (tr *TagResolver) refreshTag(ctx context.Context, tag TagResolution) error {
	// Get the current manifest digest from the registry
	newDigest, imageSize, err := tr.getManifestDigest(ctx, tag.Registry, tag.Repository, tag.Tag, tag.Platform)
	if err != nil {
		return fmt.Errorf("failed to get manifest digest: %w", err)
	}

	now := time.Now().Unix()

	// Check if digest has changed
	if newDigest != tag.PlatformDigest {
		log.Printf("Tag %s/%s:%s digest changed from %s to %s",
			tag.Registry, tag.Repository, tag.Tag, tag.PlatformDigest, newDigest)

		// Update the database
		err = exedb.WithTx(tr.db, ctx, func(ctx context.Context, queries *exedb.Queries) error {
			err := queries.UpdateTagResolutionDigest(ctx, exedb.UpdateTagResolutionDigestParams{
				PlatformDigest: &newDigest,
				LastCheckedAt:  now,
				LastChangedAt:  now,
				UpdatedAt:      now,
				ImageSize:      &imageSize,
				Registry:       tag.Registry,
				Repository:     tag.Repository,
				Tag:            tag.Tag,
				Platform:       tag.Platform,
			})
			if err != nil {
				return err
			}

			// Record history
			return queries.InsertTagResolutionHistory(ctx, exedb.InsertTagResolutionHistoryParams{
				Registry:   tag.Registry,
				Repository: tag.Repository,
				Tag:        tag.Tag,
				Platform:   tag.Platform,
				OldDigest:  &tag.PlatformDigest,
				NewDigest:  newDigest,
				ChangedAt:  now,
			})
		})
		if err != nil {
			return fmt.Errorf("failed to update tag resolution: %w", err)
		}

		// Send update notification
		select {
		case tr.updateChan <- TagUpdate{
			Registry:       tag.Registry,
			Repository:     tag.Repository,
			Tag:            tag.Tag,
			Platform:       tag.Platform,
			IndexDigest:    tag.IndexDigest,
			PlatformDigest: newDigest,
		}:
		default:
			log.Printf("Update channel full, dropping update for %s/%s:%s",
				tag.Registry, tag.Repository, tag.Tag)
		}
	} else {
		// Just update the last checked time
		err = exedb.WithTx1(tr.db, ctx, (*exedb.Queries).UpdateTagResolutionChecked, exedb.UpdateTagResolutionCheckedParams{
			LastCheckedAt: now,
			UpdatedAt:     now,
			Registry:      tag.Registry,
			Repository:    tag.Repository,
			Tag:           tag.Tag,
			Platform:      tag.Platform,
		})
		if err != nil {
			return fmt.Errorf("failed to update last checked time: %w", err)
		}
	}

	return nil
}

// ResolveTag resolves a tag to a digest, checking upstream if necessary
func (tr *TagResolver) ResolveTag(ctx context.Context, image, platform string) (string, error) {
	// Parse the image reference
	registry, repository, tag := parseImageReference(image)

	if platform == "" {
		platform = "linux/amd64"
	}

	// Check if we have a cached resolution
	resolution, err := tr.getCachedResolution(ctx, registry, repository, tag, platform)
	if err == nil && resolution != nil {
		// Check if it's still fresh
		if time.Since(resolution.LastCheckedAt) < resolution.TTL {
			// Validate the cached digest isn't a known bad one
			badDigests := []string{
				"sha256:62767d3a9e5db738ab69e115188c8e61b80ff4eb7ba70c083b2e766fe15e92e4", // Known bad digest
			}

			isBadDigest := false
			for _, bad := range badDigests {
				if resolution.PlatformDigest == bad {
					log.Printf("Cached digest %s is known to be invalid, refreshing", bad)
					isBadDigest = true
					break
				}
			}

			// Return the cached digest only if it's valid
			if !isBadDigest && resolution.PlatformDigest != "" {
				return fmt.Sprintf("%s/%s@%s", registry, repository, resolution.PlatformDigest), nil
			}
		}
	}

	// Need to fetch from upstream
	log.Printf("Fetching manifest digest for %s/%s:%s platform=%s", registry, repository, tag, platform)
	digest, imageSize, err := tr.getManifestDigest(ctx, registry, repository, tag, platform)
	if err != nil {
		return "", fmt.Errorf("failed to resolve tag: %w", err)
	}
	log.Printf("Got digest %s for %s/%s:%s", digest, registry, repository, tag)

	// Validate the digest before storing
	if digest == "sha256:62767d3a9e5db738ab69e115188c8e61b80ff4eb7ba70c083b2e766fe15e92e4" {
		log.Printf("WARNING: Got known bad digest %s, not caching", digest)
		// Still return it but don't cache it
		return fmt.Sprintf("%s/%s@%s", registry, repository, digest), nil
	}

	// Ensure digest looks valid (basic sanity check)
	if !strings.HasPrefix(digest, "sha256:") || len(digest) != 71 { // sha256: + 64 hex chars
		log.Printf("WARNING: Digest format looks invalid: %s", digest)
		return "", fmt.Errorf("invalid digest format: %s", digest)
	}

	// Store the resolution
	now := time.Now().Unix()
	ttl := 6 * time.Hour // 6 hours default

	// Adjust TTL based on tag name
	if tag == "latest" || tag == "main" || tag == "master" {
		ttl = 1 * time.Hour // 1 hour for mutable tags
	} else if strings.Contains(tag, ".") || strings.Contains(tag, "-") {
		ttl = 24 * time.Hour // 24 hours for versioned tags
	}

	err = exedb.WithTx1(tr.db, ctx, (*exedb.Queries).UpsertTagResolution, exedb.UpsertTagResolutionParams{
		Registry:       registry,
		Repository:     repository,
		Tag:            tag,
		Platform:       platform,
		PlatformDigest: &digest,
		LastCheckedAt:  now,
		LastChangedAt:  now,
		TtlSeconds:     int64(ttl.Seconds()),
		ImageSize:      &imageSize,
		CreatedAt:      now,
		UpdatedAt:      now,
	})
	if err != nil {
		log.Printf("Failed to store tag resolution: %v", err)
	}

	return fmt.Sprintf("%s/%s@%s", registry, repository, digest), nil
}

// getCachedResolution retrieves a cached tag resolution from the database
func (tr *TagResolver) getCachedResolution(ctx context.Context, registry, repository, tag, platform string) (*TagResolution, error) {
	var res TagResolution
	var lastCheckedUnix, lastChangedUnix int64
	var found bool

	err := exedb.WithRx(tr.db, ctx, func(ctx context.Context, queries *exedb.Queries) error {
		result, err := queries.GetTagResolution(ctx, exedb.GetTagResolutionParams{
			Registry:   registry,
			Repository: repository,
			Tag:        tag,
			Platform:   platform,
		})
		if err != nil {
			if strings.Contains(err.Error(), "no rows") {
				found = false
				return nil
			}
			return err
		}

		res.Registry = result.Registry
		res.Repository = result.Repository
		res.Tag = result.Tag
		res.Platform = result.Platform
		res.IndexDigest = result.IndexDigest
		res.PlatformDigest = result.PlatformDigest
		res.LastCheckedAt = time.Unix(result.LastCheckedAt, 0)
		res.LastChangedAt = time.Unix(result.LastChangedAt, 0)
		res.TTL = time.Duration(result.TtlSeconds) * time.Second
		res.SeenOnHosts = int(result.SeenOnHosts)
		res.ImageSize = result.ImageSize
		lastCheckedUnix = result.LastCheckedAt
		lastChangedUnix = result.LastChangedAt
		found = true
		return nil
	})
	if err != nil {
		return nil, err
	}

	if !found {
		return nil, nil
	}

	res.LastCheckedAt = time.Unix(lastCheckedUnix, 0)
	res.LastChangedAt = time.Unix(lastChangedUnix, 0)

	return &res, nil
}

// getManifestDigest fetches the manifest digest from a registry
func (tr *TagResolver) getManifestDigest(ctx context.Context, registry, repository, tag, platform string) (string, int64, error) {
	// Docker Hub uses registry-1.docker.io for the actual registry
	if registry == "docker.io" {
		registry = "registry-1.docker.io"
	}

	// Build the manifest URL
	url := fmt.Sprintf("https://%s/v2/%s/manifests/%s", registry, repository, tag)
	log.Printf("Fetching manifest from URL: %s", url)

	// Use GET instead of HEAD for better compatibility
	// Docker Hub doesn't return digest headers on HEAD requests
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", 0, fmt.Errorf("failed to create request: %w", err)
	}

	// Accept both OCI and Docker manifest formats
	// Prefer manifest lists/indexes for multi-platform support
	req.Header.Set("Accept", "application/vnd.docker.distribution.manifest.list.v2+json")
	req.Header.Add("Accept", "application/vnd.oci.image.index.v1+json")
	req.Header.Add("Accept", "application/vnd.docker.distribution.manifest.v2+json")
	req.Header.Add("Accept", "application/vnd.oci.image.manifest.v1+json")

	// Docker Hub always requires authentication, even for public images
	if registry == "docker.io" || registry == "registry-1.docker.io" {
		log.Printf("Docker Hub detected, getting auth token for %s/%s", registry, repository)
		token, err := tr.getAuthToken(ctx, registry, repository)
		if err != nil {
			log.Printf("Warning: Failed to get Docker Hub token: %v", err)
		} else if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
			log.Printf("Added Docker Hub auth token to initial request")
		}
	} else if auth := tr.getRegistryAuth(registry); auth != nil {
		if auth.Token != "" {
			req.Header.Set("Authorization", "Bearer "+auth.Token)
		} else if auth.Username != "" && auth.Password != "" {
			req.SetBasicAuth(auth.Username, auth.Password)
		}
	}

	resp, err := tr.httpClient.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("failed to fetch manifest: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		log.Printf("Got %d response, attempting authentication for %s/%s", resp.StatusCode, registry, repository)
		// Try to get auth token
		token, err := tr.getAuthToken(ctx, registry, repository)
		if err != nil {
			return "", 0, fmt.Errorf("failed to authenticate: %w", err)
		}

		// Create new request with token
		req, err = http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return "", 0, fmt.Errorf("failed to create authenticated request: %w", err)
		}

		// Set Accept headers again
		req.Header.Set("Accept", "application/vnd.docker.distribution.manifest.list.v2+json")
		req.Header.Add("Accept", "application/vnd.oci.image.index.v1+json")
		req.Header.Add("Accept", "application/vnd.docker.distribution.manifest.v2+json")
		req.Header.Add("Accept", "application/vnd.oci.image.manifest.v1+json")

		// Add auth token
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
			log.Printf("Added auth token to request")
		}

		resp, err = tr.httpClient.Do(req)
		if err != nil {
			return "", 0, fmt.Errorf("failed to fetch manifest with auth: %w", err)
		}
		defer resp.Body.Close()
	}

	if resp.StatusCode != http.StatusOK {
		return "", 0, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	// Always read the body for GET requests since we might need it
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", 0, fmt.Errorf("failed to read manifest body: %w", err)
	}

	// Extract digest from header
	digest := resp.Header.Get("Docker-Content-Digest")
	if digest == "" {
		// Some registries use different header names
		digest = resp.Header.Get("Digest")
	}

	contentType := resp.Header.Get("Content-Type")
	log.Printf("Response status: %d, Content-Type: %s, Header digest: %s, Body length: %d",
		resp.StatusCode, contentType, digest, len(body))

	// Debug: log first 200 chars of body if it's HTML
	if strings.Contains(contentType, "text/html") {
		preview := string(body)
		if len(preview) > 200 {
			preview = preview[:200]
		}
		log.Printf("WARNING: Got HTML response, preview: %s", preview)
		return "", 0, fmt.Errorf("got HTML response instead of manifest, likely auth failure")
	}

	var imageSize int64

	// Check if this is a multi-platform manifest
	if strings.Contains(contentType, "manifest.list") || strings.Contains(contentType, "image.index") {
		log.Printf("Detected multi-platform manifest, extracting platform-specific digest for %s", platform)
		// This is a multi-platform manifest, extract the platform-specific digest
		platformDigest, size := tr.extractPlatformDigest(body, platform)
		if platformDigest != "" {
			log.Printf("Extracted platform-specific digest: %s (replacing index digest: %s)", platformDigest, digest)
			// Return the platform-specific digest
			return platformDigest, size, nil
		}
		log.Printf("Failed to extract platform-specific digest, falling back")
	}

	// If no digest in header, compute it
	if digest == "" {
		hash := sha256.Sum256(body)
		digest = fmt.Sprintf("sha256:%x", hash)
		log.Printf("Computed digest from body: %s", digest)
	}

	// Try to get size
	var manifest struct {
		Config struct {
			Size int64 `json:"size"`
		} `json:"config"`
	}
	if err := json.Unmarshal(body, &manifest); err == nil && manifest.Config.Size > 0 {
		imageSize = manifest.Config.Size
	} else if sizeStr := resp.Header.Get("Content-Length"); sizeStr != "" {
		fmt.Sscanf(sizeStr, "%d", &imageSize)
	} else {
		imageSize = int64(len(body))
	}

	return digest, imageSize, nil
}

// getAuthToken obtains an authentication token for a registry
func (tr *TagResolver) getAuthToken(ctx context.Context, registry, repository string) (string, error) {
	// For Docker Hub
	if registry == "docker.io" || registry == "registry-1.docker.io" {
		url := fmt.Sprintf("https://auth.docker.io/token?service=registry.docker.io&scope=repository:%s:pull", repository)

		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return "", err
		}

		resp, err := tr.httpClient.Do(req)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()

		var tokenResp struct {
			Token string `json:"token"`
		}

		if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
			return "", err
		}

		return tokenResp.Token, nil
	}

	// For GitHub Container Registry (ghcr.io)
	if registry == "ghcr.io" {
		// GitHub uses Bearer token auth with a specific token endpoint
		// For anonymous access to public images, we need to get a token from their auth service
		scope := fmt.Sprintf("repository:%s:pull", repository)
		url := fmt.Sprintf("https://ghcr.io/token?scope=%s&service=ghcr.io", scope)

		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return "", err
		}

		resp, err := tr.httpClient.Do(req)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("failed to get token from ghcr.io: status %d", resp.StatusCode)
		}

		var tokenResp struct {
			Token string `json:"token"`
		}

		if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
			return "", err
		}

		return tokenResp.Token, nil
	}

	// For Quay.io
	if registry == "quay.io" {
		// Quay.io allows anonymous access to public repositories
		// Try without authentication first (will work for public images)
		return "", nil
	}

	// For other registries that might support anonymous access
	// Return empty token to attempt anonymous access
	log.Printf("Registry %s not explicitly supported, attempting anonymous access", registry)
	return "", nil
}

// getRegistryAuth returns authentication details for a registry
func (tr *TagResolver) getRegistryAuth(registry string) *RegistryAuth {
	// This would typically load from config or environment
	// For now, return nil to use anonymous access
	return nil
}

// extractPlatformDigest extracts the platform-specific digest from a manifest list/index
func (tr *TagResolver) extractPlatformDigest(manifestBody []byte, platform string) (string, int64) {
	// Parse platform string (e.g., "linux/amd64")
	parts := strings.Split(platform, "/")
	if len(parts) != 2 {
		return "", 0
	}
	os := parts[0]
	arch := parts[1]

	// Try to parse as Docker manifest list
	var manifestList struct {
		Manifests []struct {
			Digest   string `json:"digest"`
			Size     int64  `json:"size"`
			Platform struct {
				Architecture string `json:"architecture"`
				OS           string `json:"os"`
			} `json:"platform"`
		} `json:"manifests"`
	}

	if err := json.Unmarshal(manifestBody, &manifestList); err == nil {
		// Log what we found for debugging
		log.Printf("Found manifest list with %d manifests", len(manifestList.Manifests))

		// Find matching platform
		for _, m := range manifestList.Manifests {
			log.Printf("Manifest platform: OS=%s, Arch=%s, Digest=%s", m.Platform.OS, m.Platform.Architecture, m.Digest)
			if m.Platform.OS == os && m.Platform.Architecture == arch {
				log.Printf("Found matching platform manifest: %s", m.Digest)
				return m.Digest, m.Size
			}
		}
		log.Printf("No matching platform found for %s/%s", os, arch)
	}

	// Try to parse as OCI index
	var ociIndex struct {
		Manifests []struct {
			Digest   string `json:"digest"`
			Size     int64  `json:"size"`
			Platform struct {
				Architecture string `json:"architecture"`
				OS           string `json:"os"`
			} `json:"platform,omitempty"`
		} `json:"manifests"`
	}

	if err := json.Unmarshal(manifestBody, &ociIndex); err == nil && len(ociIndex.Manifests) > 0 {
		log.Printf("Found OCI index with %d manifests", len(ociIndex.Manifests))

		// Find matching platform
		for _, m := range ociIndex.Manifests {
			log.Printf("OCI manifest platform: OS=%s, Arch=%s, Digest=%s", m.Platform.OS, m.Platform.Architecture, m.Digest)
			if m.Platform.OS == os && m.Platform.Architecture == arch {
				log.Printf("Found matching OCI platform manifest: %s", m.Digest)
				return m.Digest, m.Size
			}
		}
		log.Printf("No matching OCI platform found for %s/%s", os, arch)
	}

	return "", 0
}

// parseImageReference parses an image reference into registry, repository, and tag
func parseImageReference(image string) (registry, repository, tag string) {
	// Default values
	registry = "docker.io"
	tag = "latest"

	// Remove any digest suffix for this parsing
	if idx := strings.Index(image, "@"); idx != -1 {
		image = image[:idx]
	}

	// Handle localhost:port special case first
	if strings.HasPrefix(image, "localhost:") {
		// localhost:5000/myimage:tag -> registry=localhost:5000, repo=myimage, tag=tag
		parts := strings.SplitN(image, "/", 2)
		if len(parts) == 2 {
			registry = parts[0] // localhost:5000
			image = parts[1]    // myimage:tag or myimage
		} else {
			// Just localhost:port with no image? Odd but handle it
			registry = image
			repository = ""
			return registry, repository, tag
		}

		// Now extract tag from the remaining part
		if idx := strings.LastIndex(image, ":"); idx != -1 {
			repository = image[:idx]
			tag = image[idx+1:]
		} else {
			repository = image
		}
		return registry, repository, tag
	}

	// Split by colon to separate tag (but watch for registry:port)
	if idx := strings.LastIndex(image, ":"); idx != -1 {
		// Check if this colon is part of a registry:port or a :tag
		beforeColon := image[:idx]
		afterColon := image[idx+1:]

		// If after colon looks like a port number or tag
		if !strings.Contains(afterColon, "/") {
			// It's likely a tag
			image = beforeColon
			tag = afterColon
		}
	}

	// Check if registry is specified
	if strings.Contains(image, ".") || strings.HasPrefix(image, "localhost") {
		// Looks like a registry is specified
		parts := strings.SplitN(image, "/", 2)
		if len(parts) == 2 {
			registry = parts[0]
			repository = parts[1]
		} else {
			repository = image
		}
	} else if strings.Contains(image, "/") {
		// No registry specified, use default
		repository = image
	} else {
		// Just an image name, add library/ prefix for Docker Hub
		repository = "library/" + image
	}

	return registry, repository, tag
}

// IncrementSeenOnHosts increments the seen_on_hosts counter for a resolution
func (tr *TagResolver) IncrementSeenOnHosts(ctx context.Context, registry, repository, tag, platform string) error {
	return exedb.WithTx1(tr.db, ctx, (*exedb.Queries).IncrementSeenOnHosts, exedb.IncrementSeenOnHostsParams{
		Registry:   registry,
		Repository: repository,
		Tag:        tag,
		Platform:   platform,
	})
}

// DeleteTag removes a tag resolution from the cache, forcing a fresh lookup next time
func (tr *TagResolver) DeleteTag(ctx context.Context, registry, repository, tag string) error {
	return exedb.WithTx1(tr.db, ctx, (*exedb.Queries).DeleteTagResolution, exedb.DeleteTagResolutionParams{
		Registry:   registry,
		Repository: repository,
		Tag:        tag,
	})
}
