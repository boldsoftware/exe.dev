package container

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"exe.dev/sshpool"
	"exe.dev/tagresolver"
	"github.com/regclient/regclient"
	"github.com/regclient/regclient/types/manifest"
	"github.com/regclient/regclient/types/platform"
	"github.com/regclient/regclient/types/ref"
	"golang.org/x/crypto/ssh"
)

// NerdctlManager manages containers using nerdctl with containerd
//
// Our stack is:
//
//	exed ssh's to a container host where
//	nerdctl creates containers by talking to
//	containerd which uses
//	Kata containers which starts a VM using
//	cloud-hypervisor
//
// There's also Nydus and virtio-fs.
type NerdctlManager struct {
	config   *Config
	hosts    []string // List of containerd host addresses (SSH hostnames or "local")
	hostArch string   // Cached host architecture (e.g., "arm64", "amd64")

	// perHostOperationLock serializes nerdctl create/delete operations per host
	// to prevent Kata + CNI netlink race conditions. Only one create OR delete
	// operation runs at a time per host.
	perHostOperationLock struct {
		mu    sync.Mutex
		locks map[string]*sync.Mutex
	}

	mu      sync.Mutex
	sshPool *sshpool.Pool // Pool of persistent SSH connections

	// Tag resolver for image digest management (optional)
	tagResolver *tagresolver.TagResolver
	hostUpdater *tagresolver.HostUpdater

	// Cache for gateway IP per host
	gatewayIPCache map[string]string
}

// SetTagResolver sets the tag resolver for the manager
func (m *NerdctlManager) SetTagResolver(resolver *tagresolver.TagResolver) {
	m.tagResolver = resolver
}

// SetHostUpdater sets the host updater for the manager
func (m *NerdctlManager) SetHostUpdater(updater *tagresolver.HostUpdater) {
	m.hostUpdater = updater
}

func (m *NerdctlManager) Config() Config {
	return *m.config
}

// NewNerdctlManager creates a new nerdctl-based container manager
func NewNerdctlManager(config *Config) (*NerdctlManager, error) {
	if err := validateConfig(config); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	manager := &NerdctlManager{
		config:         config,
		hosts:          config.ContainerdAddresses,
		sshPool:        sshpool.New(),
		gatewayIPCache: make(map[string]string),
	}

	// Get and cache the host architecture once (it never changes)
	slog.Info("Getting host architecture on first container host")
	if len(config.ContainerdAddresses) > 0 {
		arch, err := manager.getHostArch(context.Background(), config.ContainerdAddresses[0])
		if err != nil {
			slog.Warn("Failed to get host architecture", "error", err)
			return nil, fmt.Errorf("failed to get host architecture: %w", err)
		} else {
			// Map architecture names
			switch arch {
			case "x86_64":
				manager.hostArch = "amd64"
			case "aarch64":
				manager.hostArch = "arm64"
			default:
				manager.hostArch = arch
			}
			slog.Info("Host architecture detected", "arch", manager.hostArch)
		}
	}

	// Discover existing containers on all hosts with timeout
	slog.Info("Discovering existing containers on container hosts")
	for _, host := range config.ContainerdAddresses {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := manager.discoverContainers(ctx, host); err != nil {
			slog.Warn("Failed to discover containers on host", "host", host, "error", err)
		}
		cancel()
	}

	return manager, nil
}

// DataPath returns a path under /data with the configured isolation subdirectory
func (m *NerdctlManager) DataPath(path string) string {
	return fmt.Sprintf("/data/%s/%s", m.config.DataSubdir, strings.TrimPrefix(path, "/"))
}

// execNerdctl executes a nerdctl command via SSH on a remote host
func (m *NerdctlManager) execNerdctl(ctx context.Context, host string, args ...string) *exec.Cmd {
	host = strings.TrimPrefix(host, "ssh://")
	if host == "" || strings.HasPrefix(host, "/") {
		panic(fmt.Sprintf("execNerdctl: no valid SSH host provided: %q", host))
	}

	// Force cgroupfs for Kata (avoid nerdctl defaulting to systemd cgroup manager)
	nerdctlArgs := []string{"sudo", "nerdctl", "--namespace", "exe", "--cgroup-manager", "cgroupfs"}
	nerdctlArgs = append(nerdctlArgs, args...)

	return m.sshPool.ExecCommand(ctx, host, nerdctlArgs...)
}

// parseImageReference parses an image reference into registry, repository, and tag components
func parseImageReference(imageRef string) (registry, repository, tag string) {
	// Remove digest if present (e.g., image@sha256:...)
	if idx := strings.Index(imageRef, "@"); idx != -1 {
		imageRef = imageRef[:idx]
	}

	// Split by the last colon to separate tag
	parts := strings.Split(imageRef, ":")
	if len(parts) > 1 {
		tag = parts[len(parts)-1]
		imageRef = strings.Join(parts[:len(parts)-1], ":")
	} else {
		tag = "latest"
	}

	// Determine registry and repository
	if strings.Contains(imageRef, "/") {
		firstSlash := strings.Index(imageRef, "/")
		possibleRegistry := imageRef[:firstSlash]

		// Check if it's a registry (contains dot or colon, or is localhost)
		if strings.Contains(possibleRegistry, ".") || strings.Contains(possibleRegistry, ":") || possibleRegistry == "localhost" {
			registry = possibleRegistry
			repository = imageRef[firstSlash+1:]
		} else {
			// No registry specified, default to docker.io
			registry = "docker.io"
			repository = imageRef
		}
	} else {
		// No slash, it's a library image on docker.io
		registry = "docker.io"
		repository = "library/" + imageRef
	}

	return registry, repository, tag
}

// inspectImage inspects an image and returns its metadata, using database cache when available
func (m *NerdctlManager) inspectImage(ctx context.Context, imageRef string) (*tagresolver.ImageConfig, error) {
	// For domain-less sha256: refs, use containerd directly instead of regclient
	if strings.HasPrefix(imageRef, "sha256:") {
		return m.inspectImageLocal(ctx, imageRef)
	}

	// Check database cache first if we have a tag resolver
	if m.tagResolver != nil {
		// Parse the image reference to extract registry, repository, and tag
		registry, repository, tag := parseImageReference(imageRef)
		platform := fmt.Sprintf("linux/%s", m.hostArch)

		// Try to get cached metadata from the database
		if cfg, err := m.tagResolver.GetImageMetadata(ctx, registry, repository, tag, platform); err == nil && cfg != nil {
			slog.Info("Using cached image metadata from DB", "image", imageRef, "user", cfg.User)
			return cfg, nil
		}
	}

	// Use regclient to inspect the image remotely
	rc := regclient.New(regclient.WithDockerCreds())
	defer rc.Close(ctx, ref.Ref{})

	// Parse the image reference
	r, err := ref.New(imageRef)
	if err != nil {
		return nil, fmt.Errorf("failed to parse image reference %s: %w", imageRef, err)
	}

	// Parse the platform
	platformStr := fmt.Sprintf("linux/%s", m.hostArch)
	plat, err := platform.Parse(platformStr)
	if err != nil {
		return nil, fmt.Errorf("failed to parse platform %s: %w", platformStr, err)
	}

	// Get the manifest for the specific platform
	manifestResp, err := rc.ManifestGet(ctx, r, regclient.WithManifestPlatform(plat))
	if err != nil {
		return nil, fmt.Errorf("regclient ManifestGet: %w", err)
	}

	// Check if this is an OCI image or Docker image manifest
	var cfg tagresolver.ImageConfig
	switch mf := manifestResp.(type) {
	case manifest.Imager:
		// Get the config blob for this image
		cd, err := mf.GetConfig()
		if err != nil {
			return nil, fmt.Errorf("failed to get config descriptor: %w", err)
		}

		// Fetch the config blob content
		configBlob, err := rc.BlobGet(ctx, r, cd)
		if err != nil {
			return nil, fmt.Errorf("failed to get config blob: %w", err)
		}
		defer configBlob.Close()

		// Read the config content
		configData, err := io.ReadAll(configBlob)
		if err != nil {
			return nil, fmt.Errorf("failed to read config blob: %w", err)
		}

		// Parse the config JSON - OCI/Docker configs have nested config field
		var configJSON struct {
			Config struct {
				Entrypoint   []string            `json:"Entrypoint"`
				Cmd          []string            `json:"Cmd"`
				User         string              `json:"User"`
				Labels       map[string]string   `json:"Labels,omitempty"`
				ExposedPorts map[string]struct{} `json:"ExposedPorts,omitempty"`
			} `json:"config"`
		}

		if err := json.Unmarshal(configData, &configJSON); err != nil {
			return nil, fmt.Errorf("failed to parse config JSON: %w", err)
		}

		cfg = tagresolver.ImageConfig{
			Entrypoint:   configJSON.Config.Entrypoint,
			Cmd:          configJSON.Config.Cmd,
			User:         configJSON.Config.User,
			Labels:       configJSON.Config.Labels,
			ExposedPorts: configJSON.Config.ExposedPorts,
		}
	default:
		return nil, fmt.Errorf("manifest type %T not supported by regclient", manifestResp)
	}

	// Store the result in the database if we have a tag resolver
	if m.tagResolver != nil {
		registry, repository, tag := parseImageReference(imageRef)
		platform := fmt.Sprintf("linux/%s", m.hostArch)
		if err := m.tagResolver.StoreImageMetadata(ctx, registry, repository, tag, platform, &cfg); err != nil {
			slog.Warn("Failed to store image metadata in DB", "error", err)
		}
	}
	slog.Info("Image metadata inspected and stored", "image", imageRef, "user", cfg.User, "entrypoint", cfg.Entrypoint, "cmd", cfg.Cmd)
	return &cfg, nil
}

// getGatewayIP gets the gateway IP address for a given host, with caching
func (m *NerdctlManager) getGatewayIP(ctx context.Context, host string) (string, error) {
	host = strings.TrimPrefix(host, "ssh://")
	m.mu.Lock()
	ip, ok := m.gatewayIPCache[host]
	m.mu.Unlock()
	if ok {
		return ip, nil
	}

	// Query the gateway IP using getent
	cmd := m.sshPool.ExecCommand(ctx, host, "sh", "-c", "getent ahostsv4 _gateway 2>/dev/null | awk '{print $1; exit}'")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to get gateway IP: %w", err)
	}

	ip = strings.TrimSpace(string(out))
	if ip == "" {
		return "", fmt.Errorf("gateway IP not found")
	}

	// Cache the result
	m.mu.Lock()
	m.gatewayIPCache[host] = ip
	m.mu.Unlock()

	slog.Info("Cached gateway IP for host", "host", host, "gateway", ip)
	return ip, nil
}

// ExecSSHCommand executes a command via SSH on a remote host
func (m *NerdctlManager) ExecSSHCommand(ctx context.Context, host string, args ...string) *exec.Cmd {
	// Parse SSH format if present
	host = strings.TrimPrefix(host, "ssh://")

	if host == "" || strings.HasPrefix(host, "/") {
		// Return a command that will fail with a clear error
		// TODO(philip): Doesn't seem very clear to me!
		cmd := exec.CommandContext(ctx, "false")
		cmd.Env = []string{"ERROR=No valid SSH host provided"}
		return cmd
	}

	sudoArgs := append([]string{"sudo"}, args...)
	return m.sshPool.ExecCommand(ctx, host, sudoArgs...)
}

func isHexString(s string) bool {
	_, err := hex.DecodeString(s)
	if err != nil {
		return false
	}
	return true
}

// discoverContainers discovers existing containers on a host
func (m *NerdctlManager) discoverContainers(ctx context.Context, host string) error {
	// List containers with their labels
	cmd := m.execNerdctl(ctx, host, "ps", "-a", "--no-trunc", "--format", "json")
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("failed to list containers: %w", err)
	}

	// Parse each line as JSON
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}

		var containerInfo struct {
			ID     string          `json:"ID"`
			Names  string          `json:"Names"` // nerdctl returns a single string, not array
			Labels json.RawMessage `json:"Labels"`
			Status string          `json:"Status"`
			Image  string          `json:"Image"`
		}

		if err := json.Unmarshal([]byte(line), &containerInfo); err != nil {
			slog.Warn("Failed to parse container info", "error", err)
			continue
		}

		// Decode Labels which may be a map or a string of comma-separated key=value pairs
		// TODO: nerdctl seems to just concatenate labels with commas without appropriate escaping.
		labels := map[string]string{}
		if len(containerInfo.Labels) > 0 && string(containerInfo.Labels) != "null" {
			// Try string form: "k=v,k2=v2"
			var s string
			if err := json.Unmarshal(containerInfo.Labels, &s); err == nil {
				s = strings.TrimSpace(s)
				if s != "" {
					parts := strings.Split(s, ",")
					for _, p := range parts {
						kv := strings.SplitN(strings.TrimSpace(p), "=", 2)
						if len(kv) == 2 {
							labels[kv[0]] = kv[1]
						}
					}
				}
			}
		}

		// Only track containers managed by exe
		if labels["managed_by"] != "exe" {
			continue
		}
	}

	return nil
}

// SelectHost selects a host from available hosts (round-robin for now)
func (m *NerdctlManager) SelectHost(allocID string) (string, error) {
	if len(m.hosts) == 0 {
		return "", fmt.Errorf("no container hosts configured")
	}

	// TODO: it is *critical* we have a stable mapping of allocID -> hostname.
	// So much so, that if the host disappears, the allocID should continue
	// to map to the missing host.
	_ = allocID // TODO
	return m.hosts[0], nil
}

// acquireOperationLock acquires an exclusive lock for nerdctl create/delete operations on a host.
// This prevents Kata + CNI netlink race conditions by ensuring only one operation runs at a time.
// Returns a release function that must be called (typically via defer) to release the lock.
func (m *NerdctlManager) acquireOperationLock(host string) func() {
	if host == "" {
		panic("acquireOperationLock: empty host")
	}

	// Get or create the per-host mutex
	m.perHostOperationLock.mu.Lock()
	if m.perHostOperationLock.locks == nil {
		m.perHostOperationLock.locks = make(map[string]*sync.Mutex)
	}
	hostMutex := m.perHostOperationLock.locks[host]
	if hostMutex == nil {
		hostMutex = &sync.Mutex{}
		m.perHostOperationLock.locks[host] = hostMutex
	}
	m.perHostOperationLock.mu.Unlock()

	// Acquire the per-host lock (blocks until available)
	hostMutex.Lock()

	// Return a function to release the lock
	return func() {
		hostMutex.Unlock()
	}
}

// GetHosts returns the list of configured container hosts
func (m *NerdctlManager) GetHosts() []string {
	return m.hosts
}

// reportProgress is a helper function to report progress through the appropriate callback
func reportProgress(req *CreateContainerRequest, phase CreateProgress, imageBytes, downloadedBytes int64, message string) {
	if req.ProgressCallback != nil {
		req.ProgressCallback(CreateProgressInfo{
			Phase:           phase,
			ImageBytes:      imageBytes,
			DownloadedBytes: downloadedBytes,
			Message:         message,
		})
	}
}

// pullProgress tracks the progress of a pull operation
type pullProgress struct {
	totalBytes      int64
	downloadedBytes int64
	layers          map[string]*layerProgress
}

// layerProgress tracks progress for a single layer
type layerProgress struct {
	status  string
	current int64
	total   int64
}

// parseByteSize parses size strings like "1.2 MiB", "403.0 B", "16.2 MiB"
func parseByteSize(sizeStr string) int64 {
	sizeStr = strings.TrimSpace(sizeStr)
	if sizeStr == "" || sizeStr == "0.0 B" {
		return 0
	}

	// Regular expression to match size format
	re := regexp.MustCompile(`^([\d.]+)\s*([KMGT]i?B|B)$`)
	matches := re.FindStringSubmatch(sizeStr)
	if len(matches) != 3 {
		return 0
	}

	value, err := strconv.ParseFloat(matches[1], 64)
	if err != nil {
		return 0
	}

	unit := matches[2]
	var multiplier float64 = 1
	switch unit {
	case "B":
		multiplier = 1
	case "KiB", "KB":
		multiplier = 1024
	case "MiB", "MB":
		multiplier = 1024 * 1024
	case "GiB", "GB":
		multiplier = 1024 * 1024 * 1024
	case "TiB", "TB":
		multiplier = 1024 * 1024 * 1024 * 1024
	}

	return int64(value * multiplier)
}

// parsePullLine parses a line from nerdctl pull output and updates progress
func parsePullLine(line string, progress *pullProgress) {
	// We could use containerd's API over a forwarded socket for structured progress, but parsing nerdctl output is simpler to start
	// Remove ANSI escape codes
	ansiRe := regexp.MustCompile(`\x1b\[[0-9;]*m`)
	line = ansiRe.ReplaceAllString(line, "")

	// Parse layer download lines like:
	// layer-sha256:xxx: downloading    |------| 0.0 B/16.2 MiB
	// Also matches index-sha256, manifest-sha256, config-sha256
	// layer-sha256:xxx: done           |++++++|
	layerRe := regexp.MustCompile(`(?:layer|index|manifest|config)-sha256:([a-f0-9]+):\s+(\w+)(?:\s+.*?([\d.]+\s*[KMGT]?i?B)/([\d.]+\s*[KMGT]?i?B))?`)
	if matches := layerRe.FindStringSubmatch(line); len(matches) >= 3 {
		layerID := matches[1]
		if len(layerID) > 12 {
			layerID = layerID[:12] // Use first 12 chars of hash
		}
		status := matches[2]

		if progress.layers == nil {
			progress.layers = make(map[string]*layerProgress)
		}

		layer := progress.layers[layerID]
		if layer == nil {
			layer = &layerProgress{}
			progress.layers[layerID] = layer
		}

		layer.status = status

		// Parse size if available (matches[3] and matches[4])
		if len(matches) == 5 && matches[3] != "" && matches[4] != "" {
			layer.current = parseByteSize(matches[3])
			layer.total = parseByteSize(matches[4])
		} else if status == "done" && layer.total > 0 {
			// If status is done and we know the total, set current to total
			layer.current = layer.total
		}

		// Update total progress
		var totalBytes int64
		var downloadedBytes int64
		for _, l := range progress.layers {
			totalBytes += l.total
			if l.status == "done" {
				downloadedBytes += l.total
			} else if l.status == "downloading" {
				downloadedBytes += l.current
			}
		}
		progress.totalBytes = totalBytes
		progress.downloadedBytes = downloadedBytes
	}
}

// pullImageWithProgress pulls an image and reports progress through the callback
func (m *NerdctlManager) pullImageWithProgress(ctx context.Context, host, image string, req *CreateContainerRequest, imageSize int64) error {
	// Build pull command
	pullArgs := []string{"--snapshotter", "nydus", "pull", image}
	pullCmd := m.execNerdctl(ctx, host, pullArgs...)

	// Get stderr pipe for progress output (nerdctl writes progress to stderr)
	stderr, err := pullCmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("failed to get stderr pipe: %w", err)
	}

	// Start the command
	if err := pullCmd.Start(); err != nil {
		return fmt.Errorf("failed to start pull command: %w", err)
	}

	// Track progress
	progress := &pullProgress{
		totalBytes: imageSize,
		layers:     make(map[string]*layerProgress),
	}

	// Create a goroutine to read and parse progress
	progressDone := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(stderr)
		lastReport := time.Now()

		for scanner.Scan() {
			line := scanner.Text()
			parsePullLine(line, progress)

			// Rate limit progress reports to max 10 per second
			if time.Since(lastReport) > 100*time.Millisecond {
				// Use the imageSize if we have it, otherwise use calculated total
				totalBytes := imageSize
				if totalBytes == 0 {
					totalBytes = progress.totalBytes
				}

				reportProgress(req, CreatePull, totalBytes, progress.downloadedBytes,
					fmt.Sprintf("Pulling image: %d/%d bytes", progress.downloadedBytes, totalBytes))
				lastReport = time.Now()
			}
		}

		if err := scanner.Err(); err != nil {
			progressDone <- fmt.Errorf("error reading progress: %w", err)
		} else {
			progressDone <- nil
		}
	}()

	// Wait for command to complete
	cmdErr := pullCmd.Wait()

	// Wait for progress reading to complete
	<-progressDone

	// Final progress report
	if cmdErr == nil {
		reportProgress(req, CreatePull, imageSize, imageSize, "Image pull complete")
	}

	return cmdErr
}

// CreateContainer creates a new container using nerdctl
// If a disk already exists for the given BoxID, it will be reused (for container recreation scenarios)
func (m *NerdctlManager) CreateContainer(ctx context.Context, req *CreateContainerRequest) (*Container, error) {
	// BoxID is required - it must be explicitly set
	if req.BoxID == 0 {
		return nil, fmt.Errorf("BoxID is required and cannot be 0")
	}

	if req.Image == "" {
		return nil, fmt.Errorf("Image is required")
	}

	// Use the host provided for this allocation
	host := req.Host
	if host == "" {
		return nil, fmt.Errorf("host is required for container creation")
	}

	// Acquire exclusive lock for this host to prevent Kata + CNI netlink races
	// Only one create/delete operation runs at a time per host
	releaseOperationLock := m.acquireOperationLock(host)
	defer releaseOperationLock()

	// Check if we're recreating a container with an existing disk
	diskExists, _ := m.VerifyDisk(ctx, host, req.BoxID)
	if diskExists {
		// TODO(philip): I'm skeptical this case has test coverage or ever runs.
		// If we're NOT recreating (no existing SSH keys), fail if disk already exists
		// This prevents accidental reuse of box IDs due to bugs
		if req.ExistingSSHKeys == nil {
			return nil, fmt.Errorf("disk already exists for box %d - possible box ID reuse bug", req.BoxID)
		}
		slog.Info("CreateContainer: Reusing existing disk for box", "boxID", req.BoxID)
	} else {
		slog.Info("CreateContainer: Creating new disk for box", "boxID", req.BoxID)
	}

	// Call progress callback if provided
	reportProgress(req, CreateInit, 0, 0, "Initializing container creation")

	// Use existing SSH keys if provided (for container recreation), otherwise generate new ones
	var sshKeys *ContainerSSHKeys
	if req.ExistingSSHKeys != nil {
		slog.Info("Using existing SSH keys for container recreation", "boxID", req.BoxID)
		sshKeys = req.ExistingSSHKeys
	} else {
		slog.Info("Generating new SSH keys for container", "boxID", req.BoxID)
		var err error
		sshKeys, err = GenerateContainerSSHKeys()
		if err != nil {
			return nil, fmt.Errorf("failed to generate SSH keys: %w", err)
		}
	}

	// Generate container name
	containerName := fmt.Sprintf("exe-%s-%s", req.AllocID, req.Name)

	// Prepare image
	image := req.Image

	// Use the proper image expansion function
	image = ExpandImageNameForContainerd(image)

	// Resolve tag to digest if we have a tag resolver
	var imageWithDigest string
	if m.tagResolver != nil {
		// Check if image already has a digest
		if !strings.Contains(image, "@sha256:") {
			// Construct full platform string (OS/arch)
			platform := fmt.Sprintf("linux/%s", m.hostArch)
			resolvedImage, err := m.tagResolver.ResolveTag(ctx, image, platform)
			if err != nil {
				slog.Warn("Failed to resolve tag to digest, using tag directly", "image", image, "error", err)
				imageWithDigest = image
			} else {
				slog.Info("Resolved image tag to digest", "from", image, "to", resolvedImage)
				imageWithDigest = resolvedImage
			}
		} else {
			imageWithDigest = image
		}
	} else {
		imageWithDigest = image
	}

	// Use the default bridge network for all containers
	// Container isolation is handled by iptables rules configured during setup
	networkName := "bridge"

	// Prepare container-specific /exe.dev directory with SSH keys
	var prep struct {
		wg                  sync.WaitGroup
		containerExeDevPath string
		errc                chan error
		needsPull           bool
		imageConfig         tagresolver.ImageConfig
	}
	prep.errc = make(chan error, 3)

	// Prepare container-specific /exe.dev directory with SSH keys
	prep.wg.Go(func() {
		path, err := m.prepareContainerExeDev(ctx, host, req.BoxID, req.Name, sshKeys)
		if err != nil {
			prep.errc <- fmt.Errorf("failed to prepare container /exe.dev: %w", err)
		} else {
			prep.containerExeDevPath = path
		}
	})
	prep.wg.Go(func() {
		// Try to inspect the image to see if it exists locally
		// This is surprisingly hard. The command I have is the best I have found.
		cmd := m.sshPool.ExecCommand(ctx, host, "sudo", "ctr", "--namespace=exe", "images", "ls", "-q", "name=="+imageWithDigest)
		out, err := cmd.CombinedOutput()
		if err != nil {
			slog.Error("ctr images ls failed", "err", err, "out", string(out))
			prep.needsPull = true
			return
		}
		if got := strings.TrimSpace(string(out)); got != imageWithDigest {
			if got != "" {
				slog.Error("ctr images ls unexpected output", "out", got, "imageWithDigest", imageWithDigest)
			}
			prep.needsPull = true
			return
		}
		prep.needsPull = false
	})
	// Image config will be retrieved AFTER the pull to ensure consistent nerdctl behavior

	// Wait for all
	prep.wg.Wait()
	if len(prep.errc) > 0 {
		return nil, <-prep.errc
	}

	// Allocate SSH port first so we can publish it
	// Use a hash of allocID and name to get a stable port in [10000,19999].
	// Ensure we never pick a privileged port (<1024).
	hash := 0
	for _, b := range []byte(req.AllocID + req.Name) {
		hash = hash*31 + int(b)
	}
	// Make the modulus positive even if hash overflowed negative
	offset := hash % 10000
	if offset < 0 {
		offset = -offset
	}
	sshPort := 10000 + offset

	runArgs := []string{
		"--snapshotter", "nydus",
		"run", "-d",
		"--runtime", "io.containerd.kata.v2",
		"--name", containerName,
		"--network", networkName,
	}

	// Run as root so sshd can read its host key when auto-starting
	runArgs = append(runArgs, "--user", "root")

	// Add remaining args
	runArgs = append(runArgs,
		// Note that unlike docker using port 0 doesn't work with nerdctl
		"--publish", fmt.Sprintf("%d:22", sshPort), // Publish SSH port
		// hostname matches the box name. We always do ".exe.dev", which doesn't
		// quiet match development's .localhost proxy mapping.
		"--hostname", req.Name+".exe.dev",
		"--dns", "8.8.8.8", // Google DNS primary
		"--dns", "8.8.4.4", // Google DNS secondary
		"--dns-search", "exe.dev", // Search domain for short names
		"--label", fmt.Sprintf("alloc_id=%s", req.AllocID),
		"--label", fmt.Sprintf("box_id=%d", req.BoxID),
		"--label", "managed_by=exe",
		"--restart", "no",

		// Each containers has its own VM, so no need for container-level isolation,
		// so we allow all privileges.
		// --privileged doesn't work and gives errors like the following.
		// Sorry, something went wrong. Error ID: 518e9577-528c-45f8-b752-a27838b9be7c
		// Error: failed to create container for box-5: exit status 1 output: time="2025-09-12T11:10:54-07:00" level=fatal msg="failed to create shim task: failed to hotplug block device &{File:/dev/loop0 Format:raw ID:drive-b85b559a5c9c9342 MmioAddr: SCSIAddr: NvdimmID: VirtPath:/dev/vda DevNo: PCIPath: Index:0 ShareRW:false ReadOnly:false Pmem:false Swap:false} error: 500  reason: [\"Error from API\",\"The disk could not be added to the VM\",\"Error from device manager\",\"Failed to parse disk image format\",\"failed to fill whole buffer\"]"
		"--cap-add=ALL",
		"--security-opt", "seccomp=unconfined",
		"--security-opt", "apparmor=unconfined",
		"--cgroupns", "private",
		"--tmpfs", "/run",
		"--tmpfs", "/run/lock",
		"--tmpfs", "/tmp",
		"--tmpfs", "/sys/fs/cgroup:rw",
	)

	// Prepare container-specific /exe.dev directory with SSH keys
	containerExeDevPath := prep.containerExeDevPath
	runArgs = append(runArgs, "-v", fmt.Sprintf("%s:/exe.dev:ro", containerExeDevPath))
	slog.Info("Mounting container-specific /exe.dev", "path", containerExeDevPath)

	// Helper function to clean up container-specific directory on failure
	cleanupContainerDir := func() {
		if containerExeDevPath != "" {
			// Remove the parent container directory (not just exe.dev)
			containerDir := filepath.Dir(containerExeDevPath)
			cleanupCmd := m.ExecSSHCommand(ctx, host, "rm", "-rf", containerDir)
			if err := cleanupCmd.Run(); err != nil {
				slog.Warn("Failed to clean up container directory", "dir", containerDir, "error", err)
			}
		}
	}

	// TODO: Add proper resource limits via cgroups and Kata labels
	// For now, skip resource limits to get basic functionality working with Cloud Hypervisor
	// Cloud Hypervisor doesn't support the dynamic resource allocation that nerdctl's
	// --memory and --cpus flags trigger. We need to use cgroup parent slices instead.

	if prep.needsPull && !strings.HasPrefix(imageWithDigest, "sha256:") {
		// Report that we're about to pull with the size we determined
		reportProgress(req, CreatePull, 0, 0, "Starting image pull")

		// Always pull with progress tracking so the user sees MB progress
		// HostUpdater does not currently provide progress callbacks.
		if err := m.pullImageWithProgress(ctx, host, imageWithDigest, req, 0); err != nil {
			// Check if it's just an "already exists" error
			pullCmd := m.execNerdctl(ctx, host, "--snapshotter", "nydus", "pull", imageWithDigest)
			if output, pullErr := pullCmd.CombinedOutput(); pullErr != nil {
				if !strings.Contains(string(output), "already exists") {
					slog.Warn("Failed to pull image", "image", imageWithDigest, "error", err, "output", string(output))
				}
			}
		}
	}

	// Get image config AFTER the pull to ensure consistent nerdctl behavior.
	// This is important because nerdctl might behave differently depending on whether
	// the image is already cached on the container host machine or not.
	cfg, err := m.inspectImage(ctx, imageWithDigest)
	if err != nil {
		return nil, fmt.Errorf("failed to get image metadata after pull: %w", err)
	}
	prep.imageConfig = *cfg

	// If using exetini, override the entrypoint and pass image user
	runArgs = append(runArgs, "--entrypoint", "/exe.dev/bin/exetini")
	if prep.imageConfig.User != "" {
		// Pass the image user to exetini via environment variable
		runArgs = append(runArgs, "--env", fmt.Sprintf("EXE_IMAGE_USER=%s", prep.imageConfig.User))
	}

	// Add the image to runArgs (must come after --entrypoint but before command args)
	runArgs = append(runArgs, imageWithDigest)

	// Now append the command/entrypoint args after the image
	var imageEntrypoint, imageCmd []string
	if req.CommandOverride == "" || req.CommandOverride == "auto" || req.CommandOverride == "none" {
		imageEntrypoint = prep.imageConfig.Entrypoint
		imageCmd = prep.imageConfig.Cmd
	}
	runArgs = append(runArgs, buildEntrypointAndCmdArgs(true, req.CommandOverride, imageEntrypoint, imageCmd)...)

	// Create and start container
	reportProgress(req, CreateStart, 0, 0, "Starting container")
	slog.Info("Creating container", "command", runArgs, "boxID", req.BoxID)

	createCmd := m.execNerdctl(ctx, host, runArgs...)
	output, err := createCmd.CombinedOutput()
	if err != nil {
		outputStr := string(output)
		cleanupContainerDir()
		return nil, fmt.Errorf("failed to create container for box-%d: %w\noutput: %s", req.BoxID, err, outputStr)
	}

	// Extract container ID from output - handle both stdout only and mixed output
	lines := strings.Split(string(output), "\n")
	containerID := ""
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		// Skip empty lines and obvious error/warning messages
		if line != "" && !strings.Contains(line, "WARN") && !strings.Contains(line, "ERROR") && !strings.Contains(line, "INFO") {
			// Container ID should be a hex string
			if len(line) >= 12 && isHexString(line) {
				containerID = line
				break
			}
		}
	}

	if containerID == "" {
		return nil, fmt.Errorf("no container ID returned from output: %s", string(output))
	}

	// Wait for container to reach "running" status and get its network info
	reportProgress(req, CreateSSH, 0, 0, "Waiting for container") // TODO: rename CreateSSH
	containerIP, err := m.waitForContainerRunning(ctx, host, containerID, networkName, cleanupContainerDir)
	if err != nil {
		return nil, err
	}

	// Mark creation as done - SSH is now ready
	reportProgress(req, CreateDone, 0, 0, "Container ready")

	// Create container record
	container := &Container{
		ID:         containerID,
		Name:       req.Name,
		AllocID:    req.AllocID,
		Status:     StatusRunning,
		Image:      image,
		CreatedAt:  time.Now(),
		PodName:    containerName,
		Namespace:  "nerdctl",
		DockerHost: host,
		// SSH key material
		SSHServerIdentityKey: sshKeys.ServerIdentityKey,
		SSHAuthorizedKeys:    sshKeys.AuthorizedKeys,
		SSHClientPrivateKey:  sshKeys.ClientPrivateKey,
		SSHPort:              sshPort,
	}

	// Default to root user if not specified in image
	if loginUser := prep.imageConfig.Labels["exe.dev/login-user"]; loginUser != "" {
		container.SSHUser = loginUser
	} else if prep.imageConfig.User != "" {
		container.SSHUser = prep.imageConfig.User
	} else {
		container.SSHUser = "root"
	}

	// Detect the best exposed port for automatic routing
	container.ExposedPorts = prep.imageConfig.ExposedPorts

	slog.Info("Created container", "id", containerID, "host", host, "ip", containerIP, "ssh_port", sshPort)

	return container, nil
}

// applyPortIsolation applies bridge port isolation to a container's network interface
// This prevents containers from communicating with each other on the same bridge
func (m *NerdctlManager) applyPortIsolation(ctx context.Context, host, containerID string) error {
	// First, ensure the bridge has VLAN filtering enabled
	enableVlanCmd := m.ExecSSHCommand(ctx, host, "sh", "-c",
		"if ip link show nerdctl0 >/dev/null 2>&1; then "+
			"if ! ip -d link show nerdctl0 2>/dev/null | grep -q 'vlan_filtering 1'; then "+
			"sudo ip link set nerdctl0 type bridge vlan_filtering 1; "+
			"fi; "+
			"fi")
	if err := enableVlanCmd.Run(); err != nil {
		// Log but don't fail - bridge might not exist yet or already configured
		slog.Error("Could not enable VLAN filtering", "error", err)
	}

	fallbackApplyAll := func() error {
		isolateAllCmd := m.ExecSSHCommand(ctx, host, "sh", "-c",
			"for dev in $(bridge link show 2>/dev/null | grep 'master nerdctl0' | cut -d: -f2 | cut -d@ -f1); do sudo bridge link set dev $dev isolated on flood off mcast_flood off bcast_flood off 2>/dev/null || true; done")
		if err := isolateAllCmd.Run(); err != nil {
			return fmt.Errorf("failed to apply port isolation to bridge ports: %w", err)
		}
		slog.Info("Applied port isolation to all bridge ports", "container", containerID)
		return nil
	}

	// Get the container's PID to find its network namespace
	pidCmd := m.execNerdctl(ctx, host, "inspect", containerID, "--format", "{{.State.Pid}}")
	pidOutput, err := pidCmd.Output()
	if err != nil {
		// Could not get PID (possible with Kata); fall back to applying to all ports
		slog.Error("Port isolation: PID unavailable, applying to all ports", "container", containerID, "error", err)
		return fallbackApplyAll()
	}
	pid := strings.TrimSpace(string(pidOutput))
	if pid == "" || pid == "0" {
		// Kata/VM-based runtimes may not expose a usable netns; apply to all ports
		slog.Error("Port isolation: empty PID, applying to all ports", "container", containerID)
		return fallbackApplyAll()
	}

	// Find the veth interface on the host side that corresponds to this container
	// The veth pair will be in the container's network namespace with a peer index
	findVethCmd := m.ExecSSHCommand(ctx, host, "sh", "-c",
		fmt.Sprintf("nsenter -t %s -n ip link show eth0 2>/dev/null | grep -o 'eth0@if[0-9]*' | grep -o '[0-9]*' || true", pid))
	vethIndexOutput, err := findVethCmd.Output()
	if err != nil {
		slog.Error("Port isolation: veth index probe failed, applying to all ports", "container", containerID, "error", err)
		return fallbackApplyAll()
	}
	vethIndex := strings.TrimSpace(string(vethIndexOutput))
	if vethIndex == "" {
		// With Kata there is no veth peer in a container netns; apply to all ports
		slog.Error("Port isolation: no veth index, applying to all ports", "container", containerID)
		return fallbackApplyAll()
	}

	// Find the corresponding veth interface on the host
	findHostVethCmd := m.ExecSSHCommand(ctx, host, "sh", "-c",
		fmt.Sprintf("ip link show | grep '^%s:' | cut -d: -f2 | cut -d@ -f1 | tr -d ' '", vethIndex))
	hostVethOutput, err := findHostVethCmd.Output()
	if err != nil {
		slog.Error("Port isolation: host veth lookup failed, applying to all ports", "container", containerID, "error", err)
		return fallbackApplyAll()
	}
	hostVeth := strings.TrimSpace(string(hostVethOutput))
	if hostVeth == "" {
		slog.Error("Port isolation: empty host veth, applying to all ports", "container", containerID)
		return fallbackApplyAll()
	}

	// Apply port isolation settings to the veth interface
	isolateCmd := m.ExecSSHCommand(ctx, host, "sh", "-c",
		fmt.Sprintf("bridge link set dev %s isolated on flood off mcast_flood off bcast_flood off 2>/dev/null || true", hostVeth))
	if err := isolateCmd.Run(); err != nil {
		return fmt.Errorf("failed to apply port isolation to %s: %w", hostVeth, err)
	}

	slog.Info("Applied port isolation", "container", containerID, "interface", hostVeth)
	return nil
}

// waitForContainerRunning waits for a container to reach "running" status and returns its IP address
// Oddly, network isolation is also applied here, so this is a required step!
func (m *NerdctlManager) waitForContainerRunning(ctx context.Context, host, containerID, networkName string, cleanupFunc func()) (string, error) {
	startTime := time.Now()
	const maxWaitTime = 30 * time.Second
	const checkInterval = 100 * time.Millisecond
	lastStatus := ""

	// This will hold the final inspect data
	var inspectData struct {
		State struct {
			Status string `json:"Status"`
			Error  string `json:"Error"`
		} `json:"State"`
		NetworkSettings struct {
			Networks map[string]struct {
				IPAddress string `json:"IPAddress"`
			} `json:"Networks"`
		} `json:"NetworkSettings"`
		Config struct {
			Hostname string `json:"Hostname"`
		} `json:"Config"`
	}

	for time.Since(startTime) < maxWaitTime {
		inspectCmd := m.execNerdctl(ctx, host, "inspect", containerID, "--format", "json")
		inspectOutput, err := inspectCmd.Output()
		if err != nil {
			slog.Warn("Failed to inspect container", "error", err)
			time.Sleep(checkInterval)
			continue
		}
		if err := json.Unmarshal(inspectOutput, &inspectData); err != nil {
			slog.Warn("Failed to parse inspect data", "error", err)
			time.Sleep(checkInterval)
			continue
		}

		status := inspectData.State.Status
		if status != lastStatus {
			slog.Info("Container status", "id", containerID, "status", status, "elapsed", time.Since(startTime).Seconds())
			lastStatus = status
		}

		// Check for terminal states
		if status == "exited" || status == "dead" {
			// Container failed to start
			slog.Error("Container failed", "id", containerID, "status", status, "error", inspectData.State.Error)
			// Try to get container logs for debugging
			logsCmd := m.execNerdctl(ctx, host, "logs", "--tail", "50", containerID)
			logs, _ := logsCmd.Output()
			if len(logs) > 0 {
				slog.Info("Container logs", "logs", string(logs))
			}
			// Temporarily keep failed container for debugging
			slog.Debug("Keeping failed container for inspection", "id", containerID)
			// m.execNerdctl(ctx, host, "rm", "-f", containerID).Run()
			// cleanupFunc()
			return "", fmt.Errorf("container failed to start, status: %s", status)
		}

		if status == "running" {
			break
		}

		time.Sleep(checkInterval)
	}

	// Check if we timed out
	if inspectData.State.Status != "running" {
		slog.Error("Container did not reach running status", "id", containerID, "timeout", maxWaitTime.Seconds(), "status", inspectData.State.Status)
		// Try to get container logs for debugging
		logsCmd := m.execNerdctl(ctx, host, "logs", "--tail", "50", containerID)
		logs, _ := logsCmd.Output()
		if len(logs) > 0 {
			slog.Info("Container logs", "logs", string(logs))
		}
		m.execNerdctl(ctx, host, "rm", "-f", containerID).Run()
		cleanupFunc()
		return "", fmt.Errorf("container stuck in %s state after %v", inspectData.State.Status, maxWaitTime)
	}

	// Apply port isolation to the container's network interface
	// This prevents containers from communicating with each other on the same bridge
	if err := m.applyPortIsolation(ctx, host, containerID); err != nil {
		slog.Warn("Failed to apply port isolation", "container", containerID, "error", err)
		// Continue anyway - the container is running
	}

	// Get container IP
	containerIP := ""
	if inspectData.NetworkSettings.Networks != nil {
		// Try to get IP from the created network first
		if netInfo, ok := inspectData.NetworkSettings.Networks[networkName]; ok {
			containerIP = netInfo.IPAddress
		}
		// If not found, try any network
		if containerIP == "" {
			for _, netInfo := range inspectData.NetworkSettings.Networks {
				if netInfo.IPAddress != "" {
					containerIP = netInfo.IPAddress
					break
				}
			}
		}
	}

	return containerIP, nil
}

// getHostArch gets the architecture of the host
func (m *NerdctlManager) getHostArch(ctx context.Context, host string) (string, error) {
	cmd := m.sshPool.ExecCommand(ctx, host, "uname", "-m")
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to get architecture: %w", err)
	}

	return strings.TrimSpace(string(output)), nil
}

// GetContainer retrieves container information directly from containerd
func (m *NerdctlManager) GetContainer(ctx context.Context, allocID, containerID string) (*Container, error) {
	// Determine which host to query
	host := ""
	if m.config != nil && len(m.config.ContainerdAddresses) > 0 {
		host = m.config.ContainerdAddresses[0]
	}

	// Get container info from containerd
	inspectCmd := m.execNerdctl(ctx, host, "inspect", containerID, "--format", "json")
	output, err := inspectCmd.Output()
	if err != nil {
		return nil, fmt.Errorf("container %s not found", containerID)
	}

	// Parse container info
	var inspectData struct {
		State struct {
			Status string `json:"Status"`
		} `json:"State"`
		Config struct {
			Labels map[string]string `json:"Labels"`
			User   string            `json:"User"`
		} `json:"Config"`
		NetworkSettings struct {
			Ports map[string][]struct {
				HostPort string `json:"HostPort"`
			} `json:"Ports"`
		} `json:"NetworkSettings"`
	}

	if err := json.Unmarshal(output, &inspectData); err != nil {
		return nil, fmt.Errorf("failed to parse container info: %w", err)
	}

	// Extract AllocID from labels if not provided
	containerAllocID := allocID
	if containerAllocID == "" && inspectData.Config.Labels != nil {
		containerAllocID = inspectData.Config.Labels["alloc_id"]
	}

	// Create container info
	container := &Container{
		ID:         containerID,
		AllocID:    containerAllocID,
		DockerHost: host,
		Status:     StatusUnknown,
	}

	// Set status based on containerd state
	switch strings.ToLower(inspectData.State.Status) {
	case "running":
		container.Status = StatusRunning
	case "exited", "stopped":
		container.Status = StatusStopped
	case "created":
		container.Status = StatusPending
	default:
		container.Status = StatusUnknown
	}

	// Extract SSH port from published ports
	if ports, ok := inspectData.NetworkSettings.Ports["22/tcp"]; ok && len(ports) > 0 {
		if port, err := strconv.Atoi(ports[0].HostPort); err == nil {
			container.SSHPort = port
		}
	}

	// Extract SSH user from config
	if inspectData.Config.User != "" {
		container.SSHUser = inspectData.Config.User
	} else {
		container.SSHUser = "root"
	}

	return container, nil
}

// StartContainer starts a stopped container
func (m *NerdctlManager) StartContainer(ctx context.Context, allocID, containerID string) error {
	container, err := m.GetContainer(ctx, allocID, containerID)
	if err != nil {
		return err
	}

	cmd := m.execNerdctl(ctx, container.DockerHost, "start", container.ID)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to start container: %w: %s", err, output)
	}

	container.Status = StatusRunning
	return nil
}

// StopContainer stops a running container
func (m *NerdctlManager) StopContainer(ctx context.Context, allocID, containerID string) error {
	container, err := m.GetContainer(ctx, allocID, containerID)
	if err != nil {
		return err
	}

	cmd := m.execNerdctl(ctx, container.DockerHost, "stop", container.ID)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to stop container: %w: %s", err, output)
	}

	container.Status = StatusStopped
	return nil
}

// DeleteContainer deletes a container and its resources
func (m *NerdctlManager) DeleteContainer(ctx context.Context, allocID, containerID string) error {
	container, err := m.GetContainer(ctx, allocID, containerID)
	if err != nil {
		return err
	}

	// Acquire exclusive lock for this host to prevent Kata + CNI netlink races
	// Only one create/delete operation runs at a time per host
	releaseOperationLock := m.acquireOperationLock(container.DockerHost)
	defer releaseOperationLock()

	// Get the box_id from container labels to identify the disk
	// We need to use json format and parse it
	inspectCmd := m.execNerdctl(ctx, container.DockerHost, "inspect", container.ID, "--format", "{{json .Config.Labels}}")
	boxIDOutput, err := inspectCmd.Output()
	var boxID int
	if err == nil && len(boxIDOutput) > 0 {
		// Parse JSON to get box_id
		var labels map[string]interface{}
		if err := json.Unmarshal(boxIDOutput, &labels); err == nil {
			if boxIDValue, ok := labels["box_id"]; ok {
				if boxIDStr, ok := boxIDValue.(string); ok {
					if parsed, err := strconv.Atoi(boxIDStr); err == nil {
						boxID = parsed
					}
				}
			}
		}
	}

	// Remove container (force removal even if running)
	cmd := m.execNerdctl(ctx, container.DockerHost, "rm", "-f", container.ID)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to delete container: %w: %s", err, output)
	}

	// Move the persistent disk to /data/exed/deleted to preserve it but prevent reuse
	// This solves the issue where recreating the database (and thus resetting box IDs)
	// would conflict with existing disks on the container host
	if boxID > 0 {
		sourcePath := m.DataPath(fmt.Sprintf("exed/containers/box-%d", boxID))
		deletedPath := m.DataPath(fmt.Sprintf("exed/deleted/box-%d", boxID))

		// Check if source disk exists
		checkCmd := m.ExecSSHCommand(ctx, container.DockerHost, "test", "-d", sourcePath)
		if err := checkCmd.Run(); err == nil {
			// Source exists, proceed with move
			// First create the deleted directory if it doesn't exist
			mkdirCmd := m.ExecSSHCommand(ctx, container.DockerHost, "mkdir", "-p", m.DataPath("exed/deleted"))
			if err := mkdirCmd.Run(); err != nil {
				slog.Warn("Failed to create deleted directory", "error", err)
			}

			// Check if destination already exists (from a previous deletion)
			checkDestCmd := m.ExecSSHCommand(ctx, container.DockerHost, "test", "-d", deletedPath)
			if err := checkDestCmd.Run(); err == nil {
				// Destination exists, append timestamp to make it unique
				timestamp := time.Now().Format("20060102-150405")
				deletedPath = m.DataPath(fmt.Sprintf("exed/deleted/box-%d-%s", boxID, timestamp))
				slog.Info("Deleted disk already exists, using timestamped path",
					"boxID", boxID, "path", deletedPath)
			}

			// Move the disk to deleted directory
			mvCmd := m.ExecSSHCommand(ctx, container.DockerHost, "mv", sourcePath, deletedPath)
			if err := mvCmd.Run(); err != nil {
				slog.Warn("Failed to move disk to deleted directory",
					"source", sourcePath, "dest", deletedPath, "error", err)
			} else {
				slog.Info("Moved disk to deleted directory",
					"boxID", boxID, "from", sourcePath, "to", deletedPath)
			}
		}
	}

	// There's only one network (I believe), so no need to clean that up.

	return nil
}

// ListContainers lists all containers for an allocation
func (m *NerdctlManager) ListContainers(ctx context.Context, allocID string) ([]*Container, error) {
	return m.listContainersWithFilter(ctx, fmt.Sprintf("label=alloc_id=%s", allocID), allocID)
}

// ListAllContainers lists all containers without filtering by allocation ID
func (m *NerdctlManager) ListAllContainers(ctx context.Context) ([]*Container, error) {
	return m.listContainersWithFilter(ctx, "label=alloc_id", "")
}

// ListContainersOnHost lists all containers on a specific host without filtering by allocation ID
func (m *NerdctlManager) ListContainersOnHost(ctx context.Context, host string) ([]*Container, error) {
	if strings.TrimSpace(host) == "" {
		return nil, fmt.Errorf("host is required for ListContainersOnHost")
	}
	return m.listContainersWithFilterOnHost(ctx, host, "label=alloc_id", "")
}

func (m *NerdctlManager) listContainersWithFilter(ctx context.Context, filter, allocID string) ([]*Container, error) {
	var containers []*Container
	for _, host := range m.hosts {
		if strings.TrimSpace(host) == "" {
			continue
		}
		hostContainers, err := m.listContainersWithFilterOnHost(ctx, host, filter, allocID)
		if err != nil {
			return nil, fmt.Errorf("failed to list containers on host %s: %w", host, err)
		}
		containers = append(containers, hostContainers...)
	}
	return containers, nil
}

func (m *NerdctlManager) listContainersWithFilterOnHost(ctx context.Context, host, filter, allocID string) ([]*Container, error) {
	cmd := m.execNerdctl(ctx, host, "ps", "-a", "--no-trunc", "--format", "json", "--filter", filter)
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to list containers: %w", err)
	}

	// Nerdctl may output nothing if there are no matching containers
	if strings.TrimSpace(string(output)) == "" {
		return []*Container{}, nil
	}

	// Parse JSON output: nerdctl may emit a JSON array or one JSON object per line
	type psEntry struct {
		ID     string `json:"ID"`
		Names  string `json:"Names"`
		Image  string `json:"Image"`
		Status string `json:"Status"`
		Labels string `json:"Labels"` // nerdctl returns labels as a comma-separated string
	}

	var entries []psEntry
	data := strings.TrimSpace(string(output))
	if data == "" {
		return []*Container{}, nil
	}
	if strings.HasPrefix(data, "[") {
		if err := json.Unmarshal([]byte(data), &entries); err != nil {
			return nil, fmt.Errorf("failed to parse container list: %w", err)
		}
	} else {
		// NDJSON-style: parse line by line
		for _, line := range strings.Split(data, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			var e psEntry
			if err := json.Unmarshal([]byte(line), &e); err != nil {
				// Skip unparsable lines but keep going
				slog.Warn("Skipping unparsable ps line", "error", err, "line", line)
				continue
			}
			entries = append(entries, e)
		}
	}

	var containers []*Container
	for _, entry := range entries {
		// Extract AllocID from labels if not provided
		containerAllocID := allocID
		if containerAllocID == "" && entry.Labels != "" {
			// Parse labels string to extract alloc_id
			// Labels are in format: "key1=value1,key2=value2,..."
			for _, label := range strings.Split(entry.Labels, ",") {
				if strings.HasPrefix(label, "alloc_id=") {
					containerAllocID = strings.TrimPrefix(label, "alloc_id=")
					break
				}
			}
		}

		container := &Container{
			ID:         entry.ID,
			AllocID:    containerAllocID,
			Name:       strings.TrimPrefix(entry.Names, fmt.Sprintf("exe-%s-", containerAllocID)),
			Image:      entry.Image,
			DockerHost: host,
			Status:     StatusUnknown,
		}

		// Set status based on containerd state
		statusStr := strings.ToLower(entry.Status)
		if strings.Contains(statusStr, "exited") || strings.Contains(statusStr, "stopped") {
			container.Status = StatusStopped
		} else if strings.Contains(statusStr, "up") || strings.Contains(statusStr, "running") {
			container.Status = StatusRunning
		}

		containers = append(containers, container)
	}

	return containers, nil
}

// executeInContainer executes a command inside a container
// Note: we've had issues with redirecting stdin with nerdctl exec
// Note: this only seems to be used in tests
func (m *NerdctlManager) executeInContainer(ctx context.Context, allocID, containerID string, command []string, stdin io.Reader, stdout, stderr io.Writer) error {
	container, err := m.GetContainer(ctx, allocID, containerID)
	if err != nil {
		return err
	}

	args := []string{"exec"}
	if stdin != nil {
		args = append(args, "-i")
	}
	args = append(args, container.ID)
	args = append(args, command...)

	cmd := m.execNerdctl(ctx, container.DockerHost, args...)

	if stdin != nil {
		cmd.Stdin = stdin
	}
	if stdout != nil {
		cmd.Stdout = stdout
	}
	if stderr != nil {
		cmd.Stderr = stderr
	}

	return cmd.Run()
}

// GetContainerLogs retrieves container logs
func (m *NerdctlManager) GetContainerLogs(ctx context.Context, allocID, containerID string, tail int) ([]string, error) {
	container, err := m.GetContainer(ctx, allocID, containerID)
	if err != nil {
		return nil, err
	}

	args := []string{"logs"}
	if tail > 0 {
		args = append(args, "--tail", fmt.Sprintf("%d", tail))
	}
	args = append(args, container.ID)

	cmd := m.execNerdctl(ctx, container.DockerHost, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("failed to get logs: %w", err)
	}

	// Split output into lines
	lines := strings.Split(string(output), "\n")
	// Remove empty last line if present
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}

	return lines, nil
}

// Close cleans up the manager
func (m *NerdctlManager) Close() error {
	// Close SSH connection pool
	m.sshPool.Close()

	return nil
}

// getRovolHash walks the embedded rovol FS for the given arch and returns
// the first half (32 hex chars) of a SHA-256 over path names and file contents.
func getRovolHash(arch string) (string, error) {
	archFS, err := GetRovolFS(arch)
	if err != nil {
		return "", fmt.Errorf("failed to get rovol FS: %w", err)
	}
	genFS, err := GetGenericRovolFS()
	if err != nil {
		return "", fmt.Errorf("failed to get generic rovol FS: %w", err)
	}

	filesSet := make(map[string]bool)
	collect := func(fsys fs.FS) error {
		return fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if path == "." || d.IsDir() {
				return nil
			}
			if !filesSet[path] {
				filesSet[path] = true
			}
			return nil
		})
	}
	if err := collect(archFS); err != nil {
		return "", fmt.Errorf("failed walking arch rovol FS: %w", err)
	}
	if err := collect(genFS); err != nil {
		return "", fmt.Errorf("failed walking generic rovol FS: %w", err)
	}

	var files []string
	for p := range filesSet {
		files = append(files, p)
	}
	sort.Strings(files)

	h := sha256.New()
	for _, p := range files {
		io.WriteString(h, p)
		h.Write([]byte{0})
		b, err := fs.ReadFile(archFS, p)
		if err != nil {
			b, err = fs.ReadFile(genFS, p)
			if err != nil {
				return "", fmt.Errorf("failed to read %s: %v", p, err)
			}
		}
		h.Write(b)
		h.Write([]byte{0})
	}

	sum := hex.EncodeToString(h.Sum(nil))
	if len(sum) < 32 {
		return "", fmt.Errorf("unexpected short sha256 hex: %q", sum)
	}
	return sum[:32], nil
}

// PrepareRovol prepares the RovolFS files on a host for later use by containers
// This should be called once per host during server setup
func (m *NerdctlManager) PrepareRovol(ctx context.Context, host string) error {
	// Get the RovolFS (arch + generic)
	archFS, err := GetRovolFS(m.hostArch)
	if err != nil {
		return fmt.Errorf("failed to get RovolFS for %s: %w", m.hostArch, err)
	}
	genFS, err := GetGenericRovolFS()
	if err != nil {
		return fmt.Errorf("failed to get generic RovolFS: %w", err)
	}

	// Use content hash for rovol directory path
	rovolHash, err := getRovolHash(m.hostArch)
	if err != nil {
		return fmt.Errorf("failed to compute rovol hash: %w", err)
	}
	remoteDir := m.DataPath(fmt.Sprintf("exed/rovol/rovol-%s", rovolHash))

	// Check if rovol already exists for this content hash
	checkCmd := m.ExecSSHCommand(ctx, host, "test", "-d", remoteDir)
	if err := checkCmd.Run(); err == nil {
		// Rovol already exists for this content hash, nothing to do
		slog.Info("RovolFS already prepared on host", "host", host, "rovolHash", rovolHash, "dir", remoteDir)
		return nil
	}

	// Create the remote directory using the SSH pool
	mkdirCmd := m.sshPool.ExecCommand(ctx, host, "sudo", "mkdir", "-p", remoteDir)
	if output, err := mkdirCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to create remote directory: %w: %s", err, output)
	}

	// Create a temp directory to stage the rovol files locally
	tempDir, err := os.MkdirTemp("", "rovol-staging-*")
	if err != nil {
		return fmt.Errorf("failed to create temp directory: %w", err)
	}
	defer os.RemoveAll(tempDir)

	// Helper to stage an FS into tempDir
	stageFS := func(fsys fs.FS, skipExisting bool) error {
		return fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if path == "." {
				return nil
			}
			localPath := filepath.Join(tempDir, path)
			if d.IsDir() {
				if err := os.MkdirAll(localPath, 0o755); err != nil {
					return fmt.Errorf("failed to create local directory %s: %w", localPath, err)
				}
				return nil
			}
			if skipExisting {
				if _, err := os.Stat(localPath); err == nil {
					return nil
				}
			}
			content, err := fs.ReadFile(fsys, path)
			if err != nil {
				return fmt.Errorf("failed to read file %s: %w", path, err)
			}
			mode := os.FileMode(0o644)
			if strings.Contains(path, "bin/") || strings.HasSuffix(path, ".so.1") {
				mode = 0o755
			}
			if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
				return fmt.Errorf("failed to create parent dir for %s: %w", localPath, err)
			}
			if err := os.WriteFile(localPath, content, mode); err != nil {
				return fmt.Errorf("failed to write file %s: %w", localPath, err)
			}
			return nil
		})
	}
	if err := stageFS(archFS, false); err != nil {
		return fmt.Errorf("failed to stage arch rovol files: %w", err)
	}
	if err := stageFS(genFS, true); err != nil {
		return fmt.Errorf("failed to stage generic rovol files: %w", err)
	}

	// Transfer the entire directory structure using the SSH pool's SCP method
	// The temp directory has a random suffix, so we need to get just the basename
	tempBaseName := filepath.Base(tempDir)
	tempRemotePath := filepath.Join("/tmp", tempBaseName)

	// First copy the temp directory to remote /tmp
	if err := m.sshPool.SCP(ctx, host, "/tmp", tempDir); err != nil {
		return fmt.Errorf("failed to transfer rovol files: %w", err)
	}

	// Now move the contents to the final location with sudo and fix ownership
	// We need to use sh -c for the && operator
	// The chown ensures all files are owned by root:root regardless of source ownership
	moveScript := fmt.Sprintf("sudo cp -rp %s/* %s && sudo chown -R root:root %s && sudo rm -rf %s",
		tempRemotePath, remoteDir, remoteDir, tempRemotePath)
	moveCmd := m.sshPool.ExecCommand(ctx, host, "sh", "-c", moveScript)
	if output, err := moveCmd.CombinedOutput(); err != nil {
		// Try to clean up
		m.sshPool.ExecCommand(ctx, host, "rm", "-rf", tempRemotePath).Run()
		return fmt.Errorf("failed to move files to final location: %w: %s", err, output)
	}

	slog.Info("Successfully prepared RovolFS on host", "host", host, "arch", m.hostArch, "rovolHash", rovolHash, "dir", remoteDir)

	// Create var/empty directory for sshd privilege separation
	// This directory must exist but remain empty
	varEmptyDir := filepath.Join(remoteDir, "var", "empty")
	varEmptyCmd := m.sshPool.ExecCommand(ctx, host, "sudo", "mkdir", "-p", varEmptyDir)
	if output, err := varEmptyCmd.CombinedOutput(); err != nil {
		slog.Warn("Failed to create var/empty directory", "error", err, "output", string(output))
		// Continue anyway - the directory might already exist
	} else {
		slog.Info("Created var/empty directory for sshd privilege separation", "dir", varEmptyDir)
	}

	return nil
}

// VerifyDisk checks if a persistent disk exists for a given box ID
func (m *NerdctlManager) VerifyDisk(ctx context.Context, host string, boxID int) (bool, error) {
	diskPath := m.DataPath(fmt.Sprintf("exed/containers/box-%d", boxID))

	// Verify directory exists (impliciltly) and has an exe.dev subdir
	verifyCmd := m.ExecSSHCommand(ctx, host, "test", "-d", filepath.Join(diskPath, "exe.dev"))
	if err := verifyCmd.Run(); err != nil {
		return false, nil // Directory exists but missing exe.dev subdirectory
	}

	return true, nil
}

// prepareContainerExeDev creates a container-specific /exe.dev directory with SSH keys
func (m *NerdctlManager) prepareContainerExeDev(ctx context.Context, host string, boxID int, boxName string, sshKeys *ContainerSSHKeys) (string, error) {
	// Base directory for this container's files - use box ID for stable path
	containerDir := m.DataPath(fmt.Sprintf("exed/containers/box-%d/exe.dev", boxID))

	slog.Info("Preparing container-specific /exe.dev directory", "dir", containerDir, "boxID", boxID)

	// Check if disk already exists (for container recreation scenarios)
	diskExists, err := m.VerifyDisk(ctx, host, boxID)
	if err != nil {
		return "", fmt.Errorf("failed to verify disk: %w", err)
	}

	if diskExists {
		slog.Info("Disk already exists for box, reusing it", "boxID", boxID, "dir", containerDir)
		// Disk already exists - DO NOT overwrite SSH keys to preserve host key continuity
		// The existing keys on disk should match what's in the database
		// We'll skip the SSH file writing below
		return containerDir, nil
	} else {
		// Use the pre-prepared rovol files (prepared during server setup)
		rovolHash, err := getRovolHash(m.hostArch)
		if err != nil {
			return "", fmt.Errorf("failed to compute rovol hash: %w", err)
		}
		rovolPath := m.DataPath(fmt.Sprintf("exed/rovol/rovol-%s", rovolHash))

		// Verify the rovol directory exists
		checkCmd := m.ExecSSHCommand(ctx, host, "test", "-d", rovolPath)
		if err := checkCmd.Run(); err != nil {
			return "", fmt.Errorf("rovol not prepared for content hash %s on host %s: PrepareRovol should have been called during server setup", rovolHash, host)
		}

		// Combine directory creation and CoW clone into a single command for speed
		// This reduces SSH round-trips significantly
		// Note: The source files should already be owned by root:root from PrepareRovol
		setupCmd := fmt.Sprintf(
			"sudo mkdir -p %s && (sudo cp -a --reflink=auto %s/. %s/ || sudo cp -a %s/. %s/) && sudo chown -R root:root %s && sudo mkdir -p %s",
			containerDir, rovolPath, containerDir, rovolPath, containerDir, containerDir, filepath.Join(containerDir, "var/empty"))

		slog.Info("Setting up new container directory with CoW clone", "from", rovolPath, "to", containerDir)
		combinedCmd := m.ExecSSHCommand(ctx, host, "sh", "-c", setupCmd)
		if output, err := combinedCmd.CombinedOutput(); err != nil {
			return "", fmt.Errorf("failed to setup container directory: %w: %s", err, output)
		}
	}

	// Now add the container-specific SSH files
	// Extract server public key from the server identity key
	serverPrivKey, err := ssh.ParsePrivateKey([]byte(sshKeys.ServerIdentityKey))
	if err != nil {
		return "", fmt.Errorf("failed to parse server private key: %w", err)
	}
	serverPubKey := string(ssh.MarshalAuthorizedKey(serverPrivKey.PublicKey()))

	// Write SSH key files
	files := map[string]struct {
		content string
		mode    string
	}{
		"etc/ssh/ssh_host_ed25519_key":     {sshKeys.ServerIdentityKey, "600"},
		"etc/ssh/ssh_host_ed25519_key.pub": {serverPubKey, "644"},
		"etc/ssh/authorized_keys":          {sshKeys.AuthorizedKeys, "644"},
	}

	// Add shelley.json if we can determine the gateway
	var gatewayURL string
	var terminalURL string
	var exedevURL string
	if m.config.IsProduction {
		gatewayURL = "https://exe.dev"
		exedevURL = "https://exe.dev"
		terminalURL = fmt.Sprintf("https://%s.xterm.exe.dev", boxName)
	} else {
		gatewayIP, err := m.getGatewayIP(ctx, host)
		terminalURL = fmt.Sprintf("http://%s.xterm.localhost:%d", boxName, m.config.ExedListeningPort)
		if err == nil {
			gatewayURL = fmt.Sprintf("http://%s:%d", gatewayIP, m.config.ExedListeningPort)
			exedevURL = fmt.Sprintf("http://localhost:%d", m.config.ExedListeningPort)
		}
	}
	shelleyJSON := map[string]interface{}{
		"terminal_url":  terminalURL,
		"default_model": "claude-sonnet-4.5",
	}
	if gatewayURL != "" {
		shelleyJSON["llm_gateway"] = gatewayURL
		shelleyJSON["key_generator"] = "sudo /usr/local/bin/generate-gateway-token"
	}
	// Add "Back to exe.dev" link if we have an exe.dev URL
	if exedevURL != "" {
		shelleyJSON["links"] = []map[string]string{
			{
				"title":    "Back to exe.dev",
				"icon_svg": "M3 12l2-2m0 0l7-7 7 7M5 10v10a1 1 0 001 1h3m10-11l2 2m-2-2v10a1 1 0 01-1 1h-3m-6 0a1 1 0 001-1v-4a1 1 0 011-1h2a1 1 0 011 1v4a1 1 0 001 1m-6 0h6",
				"url":      exedevURL,
			},
		}
	}

	shelleyJSONBytes, err := json.MarshalIndent(shelleyJSON, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to marshal shelley.json: %w", err)
	}
	files["shelley.json"] = struct {
		content string
		mode    string
	}{string(shelleyJSONBytes), "644"}

	// Build a single command to write all files
	// This dramatically reduces SSH round-trips from 4 to 1
	var writeScript strings.Builder
	// Ensure the SSH directory has correct permissions (755 for directory, readable by all)
	writeScript.WriteString(fmt.Sprintf("sudo chmod 755 '%s'", filepath.Join(containerDir, "etc/ssh")))

	for relPath, fileInfo := range files {
		fullPath := filepath.Join(containerDir, relPath)
		// Use base64 encoding to safely transfer the content
		encodedContent := base64.StdEncoding.EncodeToString([]byte(fileInfo.content))

		// Add command to write and chmod each file with sudo
		writeScript.WriteString(fmt.Sprintf(" && echo '%s' | base64 -d | sudo tee '%s' > /dev/null && sudo chmod %s '%s'",
			encodedContent, fullPath, fileInfo.mode, fullPath))
	}

	// Execute the combined command
	writeCmd := m.ExecSSHCommand(ctx, host, "sh", "-c", writeScript.String())
	if output, err := writeCmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("failed to write SSH files: %w: %s", err, output)
	}

	if gatewayURL != "" {
		slog.Info("Wrote all container-specific files", "gateway", gatewayURL)
	} else {
		slog.Info("Wrote all container-specific SSH files")
	}

	slog.Info("Successfully prepared container-specific /exe.dev directory", "dir", containerDir)
	return containerDir, nil
}

// inspectImageLocal inspects a domain-less sha256: image ref using containerd directly
func (m *NerdctlManager) inspectImageLocal(ctx context.Context, imageRef string) (*tagresolver.ImageConfig, error) {
	// Extract the actual image reference from sha256:...
	if !strings.HasPrefix(imageRef, "sha256:") {
		return nil, fmt.Errorf("not a sha256: image: %s", imageRef)
	}

	// Use the first host to inspect the image (they should all have the same image)
	host := ""
	if len(m.hosts) > 0 {
		host = m.hosts[0]
	}

	// Use nerdctl to inspect the image directly from containerd
	// nerdctl image inspect provides the OCI config
	inspectCmd := m.ExecSSHCommand(ctx, host, "nerdctl", "--namespace=exe", "image", "inspect", imageRef, "--format", "json")
	output, err := inspectCmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to inspect local image %s: %w", imageRef, err)
	}

	// Parse the ctr inspect output
	var inspectResult struct {
		Config struct {
			Entrypoint []string `json:"Entrypoint"`
			Cmd        []string `json:"Cmd"`
			User       string   `json:"User"`
		} `json:"Config"`
	}

	if err := json.Unmarshal(output, &inspectResult); err != nil {
		return nil, fmt.Errorf("failed to parse image inspect output: %w", err)
	}

	cfg := &tagresolver.ImageConfig{
		Entrypoint: inspectResult.Config.Entrypoint,
		Cmd:        inspectResult.Config.Cmd,
		User:       inspectResult.Config.User,
	}

	slog.Info("Inspected local image", "image", imageRef, "user", cfg.User, "entrypoint", cfg.Entrypoint, "cmd", cfg.Cmd)
	return cfg, nil
}
