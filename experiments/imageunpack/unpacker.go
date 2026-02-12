package imageunpack

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/pkg/archive"
	"github.com/containerd/containerd/v2/pkg/archive/compression"
	"github.com/distribution/reference"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

const (
	mediaTypeDockerManifest     = "application/vnd.docker.distribution.manifest.v2+json"
	mediaTypeDockerManifestList = "application/vnd.docker.distribution.manifest.list.v2+json"
)

// Unpacker handles fast parallel unpacking of OCI images.
type Unpacker struct {
	config *Config
	log    *slog.Logger
	client *http.Client
}

// NewUnpacker creates a new Unpacker with the given configuration.
func NewUnpacker(cfg *Config, log *slog.Logger) *Unpacker {
	if cfg == nil {
		cfg = DefaultConfig()
	}
	if log == nil {
		log = slog.Default()
	}

	transport := &http.Transport{
		MaxIdleConns:        cfg.Concurrency * 2,
		MaxIdleConnsPerHost: cfg.Concurrency * 2,
		MaxConnsPerHost:     cfg.Concurrency * 2,
		IdleConnTimeout:     0, // keep connections alive
		DisableCompression:  true,
		ForceAttemptHTTP2:   true, // HTTP/2 allows multiplexing many requests over fewer connections
	}
	if cfg.Insecure {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}

	return &Unpacker{
		config: cfg,
		log:    log,
		client: &http.Client{Transport: transport},
	}
}

// Unpack downloads and unpacks an image to the destination directory.
// It returns the image digest and other metadata.
func (u *Unpacker) Unpack(ctx context.Context, imageRef, destDir string) (*Result, error) {
	platform := Platform()
	return u.UnpackWithPlatform(ctx, imageRef, platform, destDir)
}

// UnpackWithPlatform downloads and unpacks an image for a specific platform.
func (u *Unpacker) UnpackWithPlatform(ctx context.Context, imageRef, platform, destDir string) (*Result, error) {
	// Check root requirement
	if !IsRoot() && !u.config.NoSameOwner {
		return nil, fmt.Errorf("must run as root or set NoSameOwner option (--no-same-owner flag)")
	}

	ref, err := reference.ParseNormalizedNamed(imageRef)
	if err != nil {
		return nil, fmt.Errorf("parse image reference: %w", err)
	}

	// Add latest tag if none specified
	ref = reference.TagNameOnly(ref)

	// Get registry info
	registryURL, repoPath := u.getRegistryInfo(ref)

	u.log.DebugContext(ctx, "fetching image",
		"ref", imageRef,
		"registry", registryURL,
		"repo", repoPath,
		"platform", platform)

	// Get auth token
	token, err := u.getToken(ctx, registryURL, repoPath, ref)
	if err != nil {
		return nil, fmt.Errorf("get auth token: %w", err)
	}

	// Get the tag or digest
	var tagOrDigest string
	if tagged, ok := ref.(reference.Tagged); ok {
		tagOrDigest = tagged.Tag()
	} else if digested, ok := ref.(reference.Digested); ok {
		tagOrDigest = digested.Digest().String()
	} else {
		tagOrDigest = "latest"
	}

	// Fetch manifest
	manifestURL := fmt.Sprintf("%s/v2/%s/manifests/%s", registryURL, repoPath, tagOrDigest)
	manifestData, manifestDigest, mediaType, err := u.fetchManifest(ctx, manifestURL, token)
	if err != nil {
		return nil, fmt.Errorf("fetch manifest: %w", err)
	}

	u.log.InfoContext(ctx, "manifest fetched",
		"digest", manifestDigest,
		"mediaType", mediaType,
		"size", len(manifestData))

	result := &Result{
		Digest: manifestDigest,
	}

	// Handle manifest list / image index
	switch mediaType {
	case ocispec.MediaTypeImageIndex, mediaTypeDockerManifestList:
		var idx ocispec.Index
		if err := json.Unmarshal(manifestData, &idx); err != nil {
			return nil, fmt.Errorf("unmarshal index: %w", err)
		}
		return u.unpackIndex(ctx, registryURL, repoPath, token, idx, platform, destDir, result)

	case ocispec.MediaTypeImageManifest, mediaTypeDockerManifest:
		var man ocispec.Manifest
		if err := json.Unmarshal(manifestData, &man); err != nil {
			return nil, fmt.Errorf("unmarshal manifest: %w", err)
		}
		return u.unpackManifest(ctx, registryURL, repoPath, token, man, destDir, result)

	default:
		return nil, fmt.Errorf("unsupported media type: %s", mediaType)
	}
}

func (u *Unpacker) getRegistryInfo(ref reference.Named) (string, string) {
	hostname := reference.Domain(ref)
	if hostname == "docker.io" {
		hostname = "registry-1.docker.io"
	}

	scheme := "https"
	if u.config.UseHTTP {
		scheme = "http"
	}

	registryURL := fmt.Sprintf("%s://%s", scheme, hostname)
	repoPath := reference.Path(ref)

	return registryURL, repoPath
}

func (u *Unpacker) getToken(ctx context.Context, registryURL, repoPath string, ref reference.Named) (string, error) {
	// Try to access without auth first
	testURL := fmt.Sprintf("%s/v2/", registryURL)
	req, err := http.NewRequestWithContext(ctx, "GET", testURL, nil)
	if err != nil {
		return "", err
	}

	resp, err := u.client.Do(req)
	if err != nil {
		return "", err
	}
	resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		// No auth needed
		return "", nil
	}

	if resp.StatusCode != http.StatusUnauthorized {
		return "", fmt.Errorf("unexpected status from registry: %d", resp.StatusCode)
	}

	// Parse WWW-Authenticate header
	authHeader := resp.Header.Get("WWW-Authenticate")
	realm, service, _ := parseAuthHeader(authHeader)
	if realm == "" {
		return "", fmt.Errorf("no realm in auth header")
	}

	scope := fmt.Sprintf("repository:%s:pull", repoPath)
	tokenURL := fmt.Sprintf("%s?service=%s&scope=%s", realm, service, scope)

	tokenReq, err := http.NewRequestWithContext(ctx, "GET", tokenURL, nil)
	if err != nil {
		return "", err
	}

	if u.config.Username != "" || u.config.Password != "" {
		tokenReq.SetBasicAuth(u.config.Username, u.config.Password)
	}

	tokenResp, err := u.client.Do(tokenReq)
	if err != nil {
		return "", err
	}
	defer tokenResp.Body.Close()

	if tokenResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(tokenResp.Body)
		return "", fmt.Errorf("token request failed: %d: %s", tokenResp.StatusCode, string(body))
	}

	var tokenData struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(tokenResp.Body).Decode(&tokenData); err != nil {
		return "", err
	}

	if tokenData.Token != "" {
		return tokenData.Token, nil
	}
	return tokenData.AccessToken, nil
}

func (u *Unpacker) fetchManifest(ctx context.Context, url, token string) ([]byte, string, string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, "", "", err
	}

	// Accept all manifest types
	req.Header.Set("Accept", strings.Join([]string{
		ocispec.MediaTypeImageIndex,
		ocispec.MediaTypeImageManifest,
		mediaTypeDockerManifestList,
		mediaTypeDockerManifest,
	}, ", "))

	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := u.client.Do(req)
	if err != nil {
		return nil, "", "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, "", "", fmt.Errorf("manifest fetch failed: %d: %s", resp.StatusCode, string(body))
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", "", err
	}

	digest := resp.Header.Get("Docker-Content-Digest")
	mediaType := resp.Header.Get("Content-Type")

	return data, digest, mediaType, nil
}

func (u *Unpacker) unpackIndex(ctx context.Context, registryURL, repoPath, token string,
	idx ocispec.Index, platform, destDir string, result *Result,
) (*Result, error) {
	u.log.DebugContext(ctx, "processing multi-platform index", "manifests", len(idx.Manifests))

	for _, manifest := range idx.Manifests {
		if manifest.Platform == nil {
			continue
		}
		manifestPlatform := fmt.Sprintf("%s/%s", manifest.Platform.OS, manifest.Platform.Architecture)
		if !strings.EqualFold(platform, manifestPlatform) {
			continue
		}

		u.log.InfoContext(ctx, "found matching platform",
			"platform", manifestPlatform,
			"digest", manifest.Digest.String())

		// Fetch the platform-specific manifest
		manifestURL := fmt.Sprintf("%s/v2/%s/manifests/%s", registryURL, repoPath, manifest.Digest.String())
		manifestData, manifestDigest, _, err := u.fetchManifest(ctx, manifestURL, token)
		if err != nil {
			return nil, fmt.Errorf("fetch platform manifest: %w", err)
		}

		var man ocispec.Manifest
		if err := json.Unmarshal(manifestData, &man); err != nil {
			return nil, fmt.Errorf("unmarshal manifest: %w", err)
		}

		result.Digest = manifestDigest
		return u.unpackManifest(ctx, registryURL, repoPath, token, man, destDir, result)
	}

	return nil, fmt.Errorf("no manifest found for platform %s", platform)
}

func (u *Unpacker) unpackManifest(ctx context.Context, registryURL, repoPath, token string,
	man ocispec.Manifest, destDir string, result *Result,
) (*Result, error) {
	u.log.InfoContext(ctx, "unpacking manifest", "layers", len(man.Layers))

	// Download all layers using a global worker pool
	layerData, err := u.downloadAllLayers(ctx, registryURL, repoPath, token, man.Layers)
	if err != nil {
		return nil, fmt.Errorf("download layers: %w", err)
	}

	// Unpack layers in order (CRITICAL: must be sequential and in order)
	for i, layer := range man.Layers {
		u.log.DebugContext(ctx, "unpacking layer",
			"index", i,
			"digest", layer.Digest.String(),
			"size", layer.Size)

		data := layerData[layer.Digest.String()]
		result.CompressedSize += layer.Size

		// Decompress and unpack
		bytesWritten, err := u.unpackLayer(ctx, data, destDir)
		if err != nil {
			return nil, fmt.Errorf("unpack layer %d: %w", i, err)
		}

		result.UnpackedSize += bytesWritten
		result.LayerCount++

		u.log.DebugContext(ctx, "layer unpacked",
			"index", i,
			"compressed", layer.Size,
			"uncompressed", bytesWritten)
	}

	// Fetch and parse config
	configURL := fmt.Sprintf("%s/v2/%s/blobs/%s", registryURL, repoPath, man.Config.Digest.String())
	configData, err := u.downloadBlob(ctx, configURL, token, man.Config.Size)
	if err != nil {
		return nil, fmt.Errorf("download config: %w", err)
	}

	var conf ocispec.Image
	if err := json.Unmarshal(configData, &conf); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}
	result.Config = &conf

	u.log.InfoContext(ctx, "unpack complete",
		"digest", result.Digest,
		"layers", result.LayerCount,
		"compressedSize", result.CompressedSize,
		"unpackedSize", result.UnpackedSize)

	return result, nil
}

// chunkJob represents a single chunk download job
type chunkJob struct {
	url      string
	token    string
	buf      []byte
	start    int64
	end      int64
	digest   string
	jobIndex int
	jobTotal int
}

// downloadAllLayers downloads all layers using a global worker pool
func (u *Unpacker) downloadAllLayers(ctx context.Context, registryURL, repoPath, token string,
	layers []ocispec.Descriptor,
) (map[string][]byte, error) {
	// Create buffers for all layers
	layerBuffers := make(map[string][]byte)
	for _, layer := range layers {
		layerBuffers[layer.Digest.String()] = make([]byte, layer.Size)
	}

	// Create all chunk jobs
	var jobs []chunkJob
	chunkSize := u.config.ChunkSize

	for _, layer := range layers {
		blobURL := fmt.Sprintf("%s/v2/%s/blobs/%s", registryURL, repoPath, layer.Digest.String())
		buf := layerBuffers[layer.Digest.String()]

		if layer.Size <= chunkSize {
			// Single chunk for small layers
			jobs = append(jobs, chunkJob{
				url:    blobURL,
				token:  token,
				buf:    buf,
				start:  0,
				end:    layer.Size - 1,
				digest: layer.Digest.String(),
			})
		} else {
			// Multiple chunks for large layers
			for start := int64(0); start < layer.Size; start += chunkSize {
				end := start + chunkSize - 1
				if end >= layer.Size {
					end = layer.Size - 1
				}
				jobs = append(jobs, chunkJob{
					url:    blobURL,
					token:  token,
					buf:    buf,
					start:  start,
					end:    end,
					digest: layer.Digest.String(),
				})
			}
		}
	}

	// Set job indices
	for i := range jobs {
		jobs[i].jobIndex = i + 1
		jobs[i].jobTotal = len(jobs)
	}

	u.log.DebugContext(ctx, "starting parallel download",
		"layers", len(layers),
		"totalJobs", len(jobs),
		"concurrency", u.config.Concurrency)

	// Create job channel
	jobCh := make(chan chunkJob, len(jobs))
	for _, job := range jobs {
		jobCh <- job
	}
	close(jobCh)

	// Track errors
	var firstErr atomic.Pointer[error]

	// Start workers
	var wg sync.WaitGroup
	for i := 0; i < u.config.Concurrency; i++ {
		wg.Go(func() {
			for job := range jobCh {
				// Check for prior error
				if firstErr.Load() != nil {
					return
				}

				u.log.DebugContext(ctx, "downloading chunk",
					"job", fmt.Sprintf("%d/%d", job.jobIndex, job.jobTotal),
					"digest", job.digest[:12],
					"range", fmt.Sprintf("%d-%d", job.start, job.end),
					"size", job.end-job.start+1)

				err := u.downloadChunk(ctx, job.url, job.token, job.buf, job.start, job.end)
				if err != nil {
					e := fmt.Errorf("download %s [%d-%d]: %w", job.digest[:12], job.start, job.end, err)
					firstErr.CompareAndSwap(nil, &e)
					return
				}
			}
		})
	}

	wg.Wait()

	if e := firstErr.Load(); e != nil {
		return nil, *e
	}

	return layerBuffers, nil
}

func (u *Unpacker) downloadBlob(ctx context.Context, url, token string, size int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}

	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := u.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("blob fetch failed: %d", resp.StatusCode)
	}

	return io.ReadAll(resp.Body)
}

func (u *Unpacker) downloadChunk(ctx context.Context, url, token string, buf []byte, start, end int64) error {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}

	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := u.client.Do(req)
	if err != nil {
		return err
	}
	defer func() {
		// Drain body to enable connection reuse
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}

	// Read directly into the buffer at the correct offset
	n, err := io.ReadFull(resp.Body, buf[start:end+1])
	if err != nil && err != io.ErrUnexpectedEOF {
		return fmt.Errorf("read chunk: %w (got %d bytes, expected %d)", err, n, end-start+1)
	}

	return nil
}

func (u *Unpacker) unpackLayer(ctx context.Context, data []byte, destDir string) (int64, error) {
	// Wrap in content.Reader for decompression
	cr := content.NewReader(&sizeReaderAt{reader: bytes.NewReader(data), size: int64(len(data))})

	// Decompress
	r, err := compression.DecompressStream(cr)
	if err != nil {
		return 0, fmt.Errorf("decompress: %w", err)
	}
	defer r.Close()

	// Build apply options
	var opts []archive.ApplyOpt
	if u.config.NoSameOwner {
		opts = append(opts, archive.WithNoSameOwner())
	}

	// Apply to destination
	bytesWritten, err := archive.Apply(ctx, destDir, r, opts...)
	if err != nil {
		return 0, fmt.Errorf("apply archive: %w", err)
	}

	return bytesWritten, nil
}

// Helper types and functions

type sizeReaderAt struct {
	reader *bytes.Reader
	size   int64
}

func (s *sizeReaderAt) ReadAt(p []byte, off int64) (n int, err error) {
	return s.reader.ReadAt(p, off)
}

func (s *sizeReaderAt) Close() error {
	return nil
}

func (s *sizeReaderAt) Size() int64 {
	return s.size
}

func parseAuthHeader(header string) (realm, service, scope string) {
	// Parse: Bearer realm="...",service="...",scope="..."
	header = strings.TrimPrefix(header, "Bearer ")

	for part := range strings.SplitSeq(header, ",") {
		part = strings.TrimSpace(part)
		if after, ok := strings.CutPrefix(part, "realm="); ok {
			realm = strings.Trim(after, `"`)
		} else if after, ok := strings.CutPrefix(part, "service="); ok {
			service = strings.Trim(after, `"`)
		} else if after, ok := strings.CutPrefix(part, "scope="); ok {
			scope = strings.Trim(after, `"`)
		}
	}
	return realm, service, scope
}
