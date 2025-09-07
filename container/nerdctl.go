package container

import (
	"bufio"
	"context"
	cryptorand "crypto/rand"
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
	"strconv"
	"strings"
	"sync"
	"time"

	"exe.dev/sshpool"
	"exe.dev/tagresolver"
	"golang.org/x/crypto/ssh"
)

// NerdctlManager manages containers using nerdctl with containerd
//
// ⚠️ IMPORTANT: Kata/gVisor Runtime Considerations ⚠️
// This manager MUST work with Kata runtime for security isolation.
// See setupContainerSSH() for critical warnings about exec and stdin handling.
// NEVER use 'nerdctl exec -i' with stdin redirection - it will cause containers
// to enter UNKNOWN state with Kata/gVisor runtimes.
type NerdctlManager struct {
	config   *Config
	hosts    []string // List of containerd host addresses (SSH hostnames or "local")
	hostArch string   // Cached host architecture (e.g., "arm64", "amd64")

	perHostCreateLimit struct {
		mu sync.Mutex
		m  map[string]chan struct{}
	}

	mu            sync.RWMutex
	sshTunnels    map[string]*exec.Cmd // containerID -> SSH tunnel command
	allocNetworks map[string]bool      // Track which alloc networks exist
	sshPool       *sshpool.Pool        // Pool of persistent SSH connections

	// Tag resolver for image digest management (optional)
	tagResolver *tagresolver.TagResolver
	hostUpdater *tagresolver.HostUpdater

	// Cache for nerdctl run --annotation support per host
	annSupport map[string]bool

	// Cache for image metadata to avoid repeated inspections
	// Key is "host:image" to handle multiple hosts with potentially different images
	imageCache   map[string]*ImageConfig
	imageCacheMu sync.RWMutex
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
		config:        config,
		hosts:         config.ContainerdAddresses,
		sshTunnels:    make(map[string]*exec.Cmd),
		allocNetworks: make(map[string]bool),
		sshPool:       sshpool.New(),
		annSupport:    make(map[string]bool),
		imageCache:    make(map[string]*ImageConfig),
	}

	// Verify Kata runtime is available on all hosts
	for _, host := range config.ContainerdAddresses {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		if err := manager.verifyKataRuntime(ctx, host); err != nil {
			cancel()
			return nil, fmt.Errorf("CRITICAL: Kata runtime not available on host %s: %w", host, err)
		}
		cancel()
	}

	// Get and cache the host architecture once (it never changes)
	if len(config.ContainerdAddresses) > 0 {
		arch, err := manager.getHostArch(context.Background(), config.ContainerdAddresses[0])
		if err != nil {
			slog.Warn("Failed to get host architecture", "error", err)
			// Default to amd64 if we can't determine
			manager.hostArch = "amd64"
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
	for _, host := range config.ContainerdAddresses {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := manager.discoverContainers(ctx, host); err != nil {
			slog.Warn("Failed to discover containers on host", "host", host, "error", err)
		}
		cancel()
	}

	return manager, nil
}

// execNerdctl executes a nerdctl command via SSH on a remote host
func (m *NerdctlManager) execNerdctl(ctx context.Context, host string, args ...string) *exec.Cmd {
	host = strings.TrimPrefix(host, "ssh://")
	if host == "" || strings.HasPrefix(host, "/") {
		panic(fmt.Sprintf("execNerdctl: no valid SSH host provided: %q", host))
	}

	// For remote hosts, use SSH with sudo
	// Use stdbuf to ensure unbuffered output for progress tracking
	// Force cgroupfs for Kata (avoid nerdctl defaulting to systemd cgroup manager)
	nerdctlArgs := []string{"stdbuf", "-o0", "-e0", "sudo", "nerdctl", "--namespace", "exe", "--cgroup-manager", "cgroupfs"}
	nerdctlArgs = append(nerdctlArgs, args...)

	return m.sshPool.ExecCommand(ctx, host, nerdctlArgs...)
}

// remoteFileExists checks if a file exists on the remote host via SSH
func (m *NerdctlManager) remoteFileExists(ctx context.Context, host, path string) bool {
	cmd := m.ExecSSHCommand(ctx, host, "test", "-f", path)
	if err := cmd.Run(); err != nil {
		return false
	}
	return true
}

// inspectImage inspects an image and returns its metadata, using cache when available
func (m *NerdctlManager) inspectImage(ctx context.Context, host, imageRef string) (*ImageConfig, error) {
	// Check cache first
	cacheKey := fmt.Sprintf("%s:%s", host, imageRef)
	m.imageCacheMu.RLock()
	if cached, ok := m.imageCache[cacheKey]; ok {
		m.imageCacheMu.RUnlock()
		slog.Info("Using cached image metadata", "image", imageRef, "user", cached.User)
		return cached, nil
	}
	m.imageCacheMu.RUnlock()

	// Try to inspect by the given reference
	inspectCmd := m.execNerdctl(ctx, host, "image", "inspect", imageRef, "--format", "json")
	if output, err := inspectCmd.Output(); err == nil {
		if cfg, perr := parseImageInspectJSON(output); perr == nil {
			// Cache the result
			m.imageCacheMu.Lock()
			m.imageCache[cacheKey] = &cfg
			m.imageCacheMu.Unlock()
			slog.Info("Image metadata inspected and cached", "image", imageRef, "user", cfg.User, "entrypoint", cfg.Entrypoint, "cmd", cfg.Cmd)
			return &cfg, nil
		}
		slog.Warn("Failed to parse image inspect JSON", "image", imageRef)
		return nil, fmt.Errorf("failed to parse image inspect JSON")
	}

	// If inspection failed and this is an exeuntu image, try to find it by ID
	// This handles the case where nydus snapshotter causes images to lose their tags
	if strings.Contains(imageRef, "exeuntu") {
		slog.Info("Failed to inspect exeuntu image by reference, searching by repository", "image", imageRef)

		// List images and find the exeuntu one
		listCmd := m.execNerdctl(ctx, host, "image", "ls", "--format", "{{.Repository}}:{{.ID}}")
		if listOutput, err := listCmd.Output(); err == nil {
			lines := strings.Split(string(listOutput), "\n")
			for _, line := range lines {
				if strings.Contains(line, "exeuntu") {
					// Extract the ID (after the last colon)
					parts := strings.Split(line, ":")
					if len(parts) >= 2 {
						imageID := parts[len(parts)-1]
						// Try to inspect by ID
						inspectByIDCmd := m.execNerdctl(ctx, host, "image", "inspect", imageID, "--format", "json")
						if output, err := inspectByIDCmd.Output(); err == nil {
							if cfg, perr := parseImageInspectJSON(output); perr == nil {
								// Cache both the original reference and the ID
								m.imageCacheMu.Lock()
								m.imageCache[cacheKey] = &cfg
								// Also cache by ID so future lookups by ID are fast
								m.imageCache[fmt.Sprintf("%s:%s", host, imageID)] = &cfg
								m.imageCacheMu.Unlock()
								slog.Info("Image metadata found by ID and cached", "id", imageID, "user", cfg.User, "entrypoint", cfg.Entrypoint, "cmd", cfg.Cmd)
								return &cfg, nil
							}
						}
					}
				}
			}
		}
	}

	return nil, fmt.Errorf("failed to inspect image %s", imageRef)
}

// supportsAnnotations checks whether nerdctl run supports --annotation on the given host.
func (m *NerdctlManager) supportsAnnotations(ctx context.Context, host string) bool {
	host = strings.TrimPrefix(host, "ssh://")
	m.mu.RLock()
	v, ok := m.annSupport[host]
	m.mu.RUnlock()
	if ok {
		return v
	}
	// Probe once: nerdctl run --help and look for "--annotation"
	cmd := m.sshPool.ExecCommand(ctx, host, "sudo", "nerdctl", "run", "--help")
	out, err := cmd.CombinedOutput()
	if err != nil {
		// Assume not supported on error; cache false to avoid repeated probes
		m.mu.Lock()
		m.annSupport[host] = false
		m.mu.Unlock()
		return false
	}
	supported := strings.Contains(string(out), "--annotation")
	m.mu.Lock()
	m.annSupport[host] = supported
	m.mu.Unlock()
	return supported
}

// ExecSSHCommand executes a command via SSH on a remote host
func (m *NerdctlManager) ExecSSHCommand(ctx context.Context, host string, args ...string) *exec.Cmd {
	// Parse SSH format if present
	host = strings.TrimPrefix(host, "ssh://")

	// Host is required - we always use SSH
	if host == "" || strings.HasPrefix(host, "/") {
		// Return a command that will fail with a clear error
		cmd := exec.CommandContext(ctx, "false")
		cmd.Env = []string{"ERROR=No valid SSH host provided"}
		return cmd
	}

	sudoArgs := append([]string{"sudo"}, args...)
	return m.sshPool.ExecCommand(ctx, host, sudoArgs...)
}

// isHexString checks if a string contains only hexadecimal characters
func isHexString(s string) bool {
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

// verifyKataRuntime verifies that Kata runtime is available and properly configured
func (m *NerdctlManager) verifyKataRuntime(ctx context.Context, host string) error {
	// Ensure nydus directories exist - they're required for the snapshotter
	// This is needed in case they were deleted or this is a fresh setup
	mkdirCmd := m.ExecSSHCommand(ctx, host, "sudo", "mkdir", "-p", "/var/lib/containerd-nydus/snapshots")
	if err := mkdirCmd.Run(); err != nil {
		slog.Warn("Failed to create nydus directories", "error", err)
		// Continue anyway - the directories might already exist
	}

	// First, do a quick check if kata-runtime binary exists
	// This is much faster than running a container
	kataCheckCmd := m.ExecSSHCommand(ctx, host, "kata-runtime", "--version")

	kataOutput, kataErr := kataCheckCmd.Output()
	if kataErr == nil {
		// Kata binary exists, now do a quick runtime check
		// Just check if the runtime is registered with containerd
		checkArgs := []string{"info", "--format", "json"}
		infoCmd := m.execNerdctl(ctx, host, checkArgs...)
		infoOutput, infoErr := infoCmd.Output()

		if infoErr == nil && strings.Contains(string(infoOutput), "kata") {
			slog.Info("Kata runtime verified via quick check", "host", host, "version", strings.TrimSpace(string(kataOutput)))
			return nil
		}
	}

	// Fall back to the full container test if quick check failed or was inconclusive
	// The most reliable way to check if Kata is available is to try using it
	// nerdctl info doesn't reliably report available runtimes

	// Try to run a simple test container with Kata runtime
	testContainerName := fmt.Sprintf("kata-test-%d", time.Now().UnixNano())

	// Build the test command with nydus snapshotter
	// Use --network none to avoid CNI issues during verification
	args := []string{"--snapshotter", "nydus", "run", "--runtime", "io.containerd.kata.v2", "--rm", "--network", "none", "--name", testContainerName, "alpine:latest", "echo", "kata-test"}

	// Best-effort cleanup in case a previous run left the name behind
	_ = m.execNerdctl(ctx, host, "rm", "-f", testContainerName).Run()

	testCmd := m.execNerdctl(ctx, host, args...)

	output, err := testCmd.CombinedOutput()
	if err != nil {
		// Check if this is a transient name collision; retry once with a random suffix
		outputStr := string(output)
		if strings.Contains(outputStr, "name \"") && strings.Contains(outputStr, "is already used") {
			// Retry with a unique name
			retryBytes := make([]byte, 4)
			_, _ = cryptorand.Read(retryBytes)
			retryName := fmt.Sprintf("%s-%s", testContainerName, hex.EncodeToString(retryBytes))
			_ = m.execNerdctl(ctx, host, "rm", "-f", retryName).Run()
			retryArgs := []string{"--snapshotter", "nydus", "run", "--runtime", "io.containerd.kata.v2", "--rm", "--network", "none", "--name", retryName, "alpine:latest", "echo", "kata-test"}
			retryCmd := m.execNerdctl(ctx, host, retryArgs...)
			if rOut, rErr := retryCmd.CombinedOutput(); rErr == nil {
				if strings.Contains(string(rOut), "kata-test") {
					slog.Info("Kata runtime successfully verified (after name collision retry)", "host", host)
					return nil
				}
			}
			// Fall through to error handling if retry did not succeed
		}

		// Check if it's because Kata isn't available
		if strings.Contains(outputStr, "not found") || strings.Contains(outputStr, "unknown runtime") ||
			strings.Contains(outputStr, "kata") || strings.Contains(outputStr, "runtime") {
			// We already checked kata-runtime binary above, so just report the error
			if kataErr != nil {
				return fmt.Errorf("Kata runtime not available: nerdctl test failed (%v) and kata-runtime binary check failed (%v)", err, kataErr)
			}
			// kata-runtime exists but nerdctl can't use it
			return fmt.Errorf("Kata runtime binary found but not usable via nerdctl: %v: %s", err, outputStr)
		}
		// Some other error
		return fmt.Errorf("failed to verify Kata runtime: %w: %s", err, outputStr)
	}

	// Check if output contains our test string
	if !strings.Contains(string(output), "kata-test") {
		return fmt.Errorf("Kata runtime test container didn't produce expected output: %s", output)
	}

	slog.Info("Kata runtime successfully verified", "host", host)
	return nil
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
		labels := map[string]string{}
		if len(containerInfo.Labels) > 0 && string(containerInfo.Labels) != "null" {
			// Try map[string]string first
			var m map[string]string
			if err := json.Unmarshal(containerInfo.Labels, &m); err == nil {
				labels = m
			} else {
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
		}

		// Only track containers managed by exe
		if labels["managed_by"] != "exe" {
			continue
		}

		// Note: Runtime information is not available via nerdctl inspect
		// We enforce Kata runtime on all new containers created by this manager
		// Existing containers discovered here may have been created with different settings
	}

	return nil
}

// parseContainerStatus converts nerdctl status to our ContainerStatus
func (m *NerdctlManager) parseContainerStatus(status string) ContainerStatus {
	status = strings.ToLower(status)
	if strings.Contains(status, "up") || strings.Contains(status, "running") {
		return StatusRunning
	}
	if strings.Contains(status, "paused") {
		return StatusPending
	}
	return StatusStopped
}

// perHostCreateLimit is the maximum simultaneous create limit.
//
// TODO: we need a different limit on easily overloaded, nested KVM
// setups like lima and production. Figuring out what those limits
// are is going to require manual experimentation.
const perHostCreateLimit = 2

// selectHost selects a host from available hosts (round-robin for now)
func (m *NerdctlManager) selectHost(ctx context.Context, allocID string) (ctrHost string, releaseFn func(), err error) {
	// For now, just return the first host
	//
	// TODO: it is *critical* we have a stable mapping of allocID -> hostname.
	// So much so, that if the host disappears, the allocID should continue
	// to map to the missing host. The best way forward here is probably having
	// CreateAlloc return the allocID.
	_ = allocID // TODO
	ctrHost = m.hosts[0]

	m.perHostCreateLimit.mu.Lock()
	if m.perHostCreateLimit.m == nil {
		m.perHostCreateLimit.m = make(map[string]chan struct{})
	}
	ch := m.perHostCreateLimit.m[ctrHost]
	if ch == nil {
		ch = make(chan struct{}, perHostCreateLimit)
		m.perHostCreateLimit.m[ctrHost] = ch
	}
	m.perHostCreateLimit.mu.Unlock()

	select {
	case ch <- struct{}{}:
	case <-ctx.Done():
		return "", nil, fmt.Errorf("selectHost: %w", ctx.Err())
	}

	releaseFn = func() {
		<-ch
	}
	return ctrHost, releaseFn, nil
}

// CreateAlloc creates the network infrastructure for an allocation
// This should be called during account verification, before any containers are created
func (m *NerdctlManager) CreateAlloc(ctx context.Context, allocID string, ipRange string) error {
	// Select a host for the allocation
	host, releaseFn, err := m.selectHost(ctx, allocID)
	if err != nil {
		return err
	}
	defer releaseFn()

	// Create the network and wait for it to be ready
	_, err = m.ensureAllocNetwork(ctx, allocID, ipRange, host)
	if err != nil {
		return fmt.Errorf("failed to create allocation network: %w", err)
	}

	slog.Info("Created allocation network", "allocID", allocID, "ipRange", ipRange)
	return nil
}

// GetHosts returns the list of configured container hosts
func (m *NerdctlManager) GetHosts() []string {
	return m.hosts
}

// ListAllocs returns all allocation networks currently on the specified host
func (m *NerdctlManager) ListAllocs(ctx context.Context, host string) ([]string, error) {
	// List all networks that match our exe-* naming pattern
	cmd := m.execNerdctl(ctx, host, "network", "ls", "--format", "{{.Name}}")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to list networks: %w", err)
	}

	var allocIDs []string
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "exe-") && line != "exe-bridge" {
			// Extract allocID from network name (exe-<allocID>)
			allocID := strings.TrimPrefix(line, "exe-")
			allocIDs = append(allocIDs, allocID)
		}
	}

	return allocIDs, nil
}

// DeleteAlloc removes the network for an allocation that is no longer in the database
func (m *NerdctlManager) DeleteAlloc(ctx context.Context, allocID string, host string) error {
	// Limit network name length, but handle shorter allocIDs
	nameLen := len(allocID)
	if nameLen > 12 {
		nameLen = 12
	}
	networkName := fmt.Sprintf("exe-%s", allocID[:nameLen])

	// First, remove any containers still using this network
	// List containers in the network
	listCmd := m.execNerdctl(ctx, host, "ps", "-a", "--format", "{{.Names}}", "--filter", fmt.Sprintf("network=%s", networkName))
	if output, err := listCmd.Output(); err == nil {
		containers := strings.Split(strings.TrimSpace(string(output)), "\n")
		for _, container := range containers {
			container = strings.TrimSpace(container)
			if container != "" {
				// Force remove the container
				rmCmd := m.execNerdctl(ctx, host, "rm", "-f", container)
				if _, err := rmCmd.Output(); err != nil {
					slog.Warn("Failed to remove container from deleted alloc", "container", container, "error", err)
				}
			}
		}
	}

	// Delete the network
	deleteCmd := m.execNerdctl(ctx, host, "network", "rm", networkName)
	if output, err := deleteCmd.CombinedOutput(); err != nil {
		// If network doesn't exist, that's fine
		if !strings.Contains(string(output), "not found") {
			return fmt.Errorf("failed to delete network: %w: %s", err, output)
		}
	}

	// Update our cache
	m.mu.Lock()
	delete(m.allocNetworks, networkName)
	m.mu.Unlock()

	slog.Info("Deleted allocation network", "allocID", allocID, "network", networkName)
	return nil
}

// ensureAllocNetwork ensures a network exists for the allocation
func (m *NerdctlManager) ensureAllocNetwork(ctx context.Context, allocID string, ipRange string, host string) (string, error) {
	// Limit network name length, but handle shorter allocIDs
	nameLen := min(len(allocID), 12)
	networkName := fmt.Sprintf("exe-%s", allocID[:nameLen])

	m.mu.Lock()
	exists := m.allocNetworks[networkName]
	m.mu.Unlock()

	if exists {
		return networkName, nil
	}

	// IP range must be provided from the database
	if ipRange == "" {
		return "", fmt.Errorf("no IP range assigned to allocation %s", allocID)
	}
	subnet := ipRange

	// Create network (idempotent). Avoid an extra ls call; treat "already exists" as success
	createCmd := m.execNerdctl(ctx, host,
		"network", "create", networkName,
		"--subnet", subnet,
		"--driver", "bridge",
	)
	created := true
	if output, err := createCmd.CombinedOutput(); err != nil {
		// Network might already exist, which is fine
		if strings.Contains(string(output), "already exists") {
			created = false
		} else {
			return "", fmt.Errorf("failed to create network: %w: %s", err, output)
		}
	}

	// If we just created the network, verify it's ready before proceeding
	// This helps avoid "Link not found" errors when Kata tries to attach to the network
	if created {
		// Verify the network bridge is actually ready
		// Sometimes there's a delay between network creation and the bridge being fully initialized
		var verified bool
		for i := 0; i < 10; i++ {
			// Use nerdctl network ls to verify the network exists and is ready
			verifyCmd := m.execNerdctl(ctx, host, "network", "ls", "--format", "{{.Name}}")
			if output, err := verifyCmd.Output(); err == nil {
				if strings.Contains(string(output), networkName) {
					// Also verify the bridge interface exists on the host
					bridgeCmd := m.ExecSSHCommand(ctx, host, "ip", "link", "show", "type", "bridge")
					if bridgeOut, err := bridgeCmd.Output(); err == nil && len(bridgeOut) > 0 {
						verified = true
						break
					}
				}
			}
			// Small delay before retry
			time.Sleep(100 * time.Millisecond)
		}

		if !verified {
			slog.Warn("Network created but verification failed", "network", networkName)
			// Continue anyway - the network might still work
		}
	}

	m.mu.Lock()
	m.allocNetworks[networkName] = true
	m.mu.Unlock()

	// Only attempt to add iptables rules on first creation to reduce SSH round-trips
	if created {
		if err := m.setupNetworkSecurity(ctx, host, subnet); err != nil {
			slog.Warn("Failed to set up network security", "network", networkName, "error", err)
		}
	}

	return networkName, nil
}

// setupNetworkSecurity sets up iptables rules to restrict network access
func (m *NerdctlManager) setupNetworkSecurity(ctx context.Context, host string, subnet string) error {
	// Build a single idempotent shell script to minimize SSH round-trips.
	// Use -C (check) to avoid duplicates, falling back to insert if not present.
	var b strings.Builder
	b.WriteString("set -e\n")
	add := func(args ...string) {
		// Build: iptables -C <rule> || iptables -I <rule>
		check := append([]string{"iptables", "-C"}, args...)
		insert := append([]string{"iptables", "-I"}, args...)
		b.WriteString(strings.Join(check, " "))
		b.WriteString(" || ")
		b.WriteString(strings.Join(insert, " "))
		b.WriteString("\n")
	}

	// Block access to host from container subnet, except established
	add("INPUT", "-s", subnet, "-j", "DROP")
	add("INPUT", "-s", subnet, "-m", "state", "--state", "ESTABLISHED,RELATED", "-j", "ACCEPT")

	// Block access to private networks (except container's own subnet)
	add("FORWARD", "-s", subnet, "-d", "192.168.0.0/16", "-j", "DROP")
	add("FORWARD", "-s", subnet, "-d", "172.16.0.0/12", "-j", "DROP")

	// Block access to metadata service
	add("FORWARD", "-s", subnet, "-d", "169.254.169.254", "-j", "DROP")

	// Block Tailscale interfaces if present
	for _, iface := range []string{"tailscale0", "utun"} {
		add("FORWARD", "-s", subnet, "-o", iface, "-j", "DROP")
		add("FORWARD", "-i", iface, "-d", subnet, "-j", "DROP")
	}

	cmd := m.ExecSSHCommand(ctx, host, "sh", "-c", b.String())
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to apply network security rules: %w: %s", err, output)
	}
	return nil
}

// reportProgress is a helper function to report progress through the appropriate callback
func reportProgress(req *CreateContainerRequest, phase CreateProgress, imageBytes, downloadedBytes int64, message string) {
	if req.ProgressCallbackEx != nil {
		req.ProgressCallbackEx(CreateProgressInfo{
			Phase:           phase,
			ImageBytes:      imageBytes,
			DownloadedBytes: downloadedBytes,
			Message:         message,
		})
	} else if req.ProgressCallback != nil {
		// Fall back to old callback for backward compatibility
		req.ProgressCallback(phase, imageBytes)
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

	// Select a host
	host, releaseFn, err := m.selectHost(ctx, req.AllocID)
	if err != nil {
		return nil, err
	}
	defer releaseFn()

	// Check if we're recreating a container with an existing disk
	diskExists, _ := m.VerifyDisk(ctx, host, req.BoxID)
	if diskExists {
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
	if image == "" {
		image = "ubuntu:latest"
	}
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

	// Always use exetini for containers. exetini is responsible for handling
	// special init chaining cases internally.
	useExetini := true
	autoStartSSH := true

	// Get the pre-created network name for this allocation
	nameLen := len(req.AllocID)
	if nameLen > 12 {
		nameLen = 12
	}
	networkName := fmt.Sprintf("exe-%s", req.AllocID[:nameLen])

	// Verify the network exists (it should have been created during account verification)
	m.mu.RLock()
	networkExists := m.allocNetworks[networkName]
	m.mu.RUnlock()

	if !networkExists {
		// Network doesn't exist in our cache - try to verify it exists on the host
		verifyCmd := m.execNerdctl(ctx, host, "network", "ls", "--format", "{{.Name}}")
		if output, err := verifyCmd.Output(); err == nil {
			if strings.Contains(string(output), networkName) {
				// Network exists on host, update our cache
				m.mu.Lock()
				m.allocNetworks[networkName] = true
				m.mu.Unlock()
				networkExists = true
			}
		}

		if !networkExists {
			return nil, fmt.Errorf("allocation network %s does not exist - was CreateAlloc called during account setup?", networkName)
		}
	}

	// Prepare container-specific /exe.dev directory with SSH keys
	var prep struct {
		wg                  sync.WaitGroup
		containerExeDevPath string
		errc                chan error
	}
	prep.errc = make(chan error, 1)

	// Prepare container-specific /exe.dev directory with SSH keys
	prep.wg.Add(1)
	go func() {
		defer prep.wg.Done()
		path, err := m.prepareContainerExeDev(ctx, host, req.BoxID, sshKeys)
		if err != nil {
			prep.errc <- fmt.Errorf("failed to prepare container /exe.dev: %w", err)
		} else {
			prep.containerExeDevPath = path
		}
	}()

	// We'll inspect the image later, after we know it exists (either locally or after pulling)
	// This avoids the inefficiency of trying to inspect before the image is available

	// Wait for all
	prep.wg.Wait()
	if len(prep.errc) > 0 {
		return nil, <-prep.errc
	}
	// Tag timings aligned with prior markers

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

	// Build run command with nydus snapshotter
	runArgs := []string{
		"--snapshotter", "nydus",
		"run", "-d",
		"--runtime", "io.containerd.kata.v2", // Use Kata for security
		"--name", containerName,
		"--network", networkName,
	}

	// Add remaining args
	if autoStartSSH {
		// Run as root so sshd can read its host key when auto-starting
		runArgs = append(runArgs, "--user", "root")
	}
	runArgs = append(runArgs,
		"--publish", fmt.Sprintf("%d:22", sshPort), // Publish SSH port
		"--hostname", req.Name, // Set hostname to match the container name
		"--dns", "8.8.8.8", // Google DNS primary
		"--dns", "8.8.4.4", // Google DNS secondary
		"--dns-search", "exe.dev", // Search domain for short names
		"--label", fmt.Sprintf("alloc_id=%s", req.AllocID),
		"--label", fmt.Sprintf("box_id=%d", req.BoxID),
		"--label", "managed_by=exe",
		"--restart", "no",
		// TEMPORARILY DISABLED: Removing all capability flags to debug issue
		// TODO: Re-enable after fixing container startup issue
	)
	// Optional: add OCI/Kata annotations if supported by nerdctl.
	// Prefer unified CLH restore annotation when a snapshot is present on host.
	if m.supportsAnnotations(ctx, host) {
		ann := make(map[string]string)
		// ann["io.katacontainers.config.hypervisor.kernel_params"] = "agent.hotplug_timeout=10s"
		ann["io.katacontainers.config.hypervisor.default_vcpus"] = "2"
		ann["io.katacontainers.config.hypervisor.default_memory"] = "4096"

		for k, v := range ann {
			runArgs = append(runArgs, "--annotation", fmt.Sprintf("%s=%s", k, v))
		}
	}

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

	// Image metadata variables - will be populated after ensuring image exists
	var imageUser string
	var imageEntrypoint []string
	var imageCmd []string

	// Determine final image reference (use digest version if available)
	finalImage := imageWithDigest
	if finalImage == "" {
		finalImage = image
	}
	// Note: We'll add the image to runArgs later, after setting --entrypoint

	// Check if image exists locally and get its size
	var imageSize int64
	var needsPull bool

	// Use the digest version for inspection if available
	imageToInspect := imageWithDigest
	if imageToInspect == "" {
		imageToInspect = image
	}

	// Try to inspect the image to see if it exists locally
	inspectSizeCmd := m.execNerdctl(ctx, host, "image", "inspect", imageToInspect, "--format", "{{.Size}}")
	if sizeOutput, err := inspectSizeCmd.Output(); err == nil {
		// Image exists locally - get its size
		sizeStr := strings.TrimSpace(string(sizeOutput))
		if size, err := strconv.ParseInt(sizeStr, 10, 64); err == nil {
			imageSize = size
		}
		needsPull = false
	} else {
		// Image doesn't exist locally - need to pull
		needsPull = true

		// Try to get manifest to determine image size before pulling
		// Use nerdctl image inspect with --mode=remote to get manifest without pulling
		manifestCmd := m.execNerdctl(ctx, host, "image", "inspect", "--mode=remote", image, "--format", "{{.Size}}")
		if manifestOutput, err := manifestCmd.Output(); err == nil {
			sizeStr := strings.TrimSpace(string(manifestOutput))
			if size, err := strconv.ParseInt(sizeStr, 10, 64); err == nil {
				imageSize = size
			}
		}
		// If we couldn't get the size from manifest, imageSize remains 0
	}

	// Only pull if needed
	if needsPull {
		// Report that we're about to pull with the size we determined
		reportProgress(req, CreatePull, imageSize, 0, "Starting image pull")

		// Always pull with progress tracking so the user sees MB progress
		// HostUpdater does not currently provide progress callbacks.
		if err := m.pullImageWithProgress(ctx, host, imageToInspect, req, imageSize); err != nil {
			// Check if it's just an "already exists" error
			pullCmd := m.execNerdctl(ctx, host, "--snapshotter", "nydus", "pull", imageToInspect)
			if output, pullErr := pullCmd.CombinedOutput(); pullErr != nil {
				if !strings.Contains(string(output), "already exists") {
					slog.Warn("Failed to pull image", "image", imageToInspect, "error", err, "output", string(output))
				}
			}
		}

		// After pull, try to get the actual size if we didn't have it before
		if imageSize == 0 {
			inspectCmd := m.execNerdctl(ctx, host, "image", "inspect", image, "--format", "{{.Size}}")
			if sizeOutput, err := inspectCmd.Output(); err == nil {
				sizeStr := strings.TrimSpace(string(sizeOutput))
				if size, err := strconv.ParseInt(sizeStr, 10, 64); err == nil {
					imageSize = size
				}
			}
		}
	}

	// Now that the image definitely exists (either it was already local or we just pulled it),
	// inspect it to get all metadata (user, entrypoint, cmd)
	if cfg, err := m.inspectImage(ctx, host, imageToInspect); err == nil {
		imageUser = cfg.User
		if useExetini && (req.CommandOverride == "" || req.CommandOverride == "auto" || req.CommandOverride == "none") {
			imageEntrypoint = cfg.Entrypoint
			imageCmd = cfg.Cmd
		}

		// Also cache by the original image name if different
		if req.Image != "" && req.Image != imageToInspect {
			cacheKey := fmt.Sprintf("%s:%s", host, req.Image)
			m.imageCacheMu.Lock()
			m.imageCache[cacheKey] = cfg
			m.imageCacheMu.Unlock()
		}
	} else {
		slog.Warn("Failed to get image metadata", "image", imageToInspect, "error", err)
	}

	// If using exetini, override the entrypoint and pass image user
	runArgs = append(runArgs, "--entrypoint", "/exe.dev/bin/exetini")
	if imageUser != "" {
		// Pass the image user to exetini via environment variable
		runArgs = append(runArgs, "--env", fmt.Sprintf("EXE_IMAGE_USER=%s", imageUser))
	}

	// Add the image to runArgs (must come after --entrypoint but before command args)
	runArgs = append(runArgs, finalImage)

	// Now append the command/entrypoint args after the image
	runArgs = append(runArgs, buildEntrypointAndCmdArgs(true, req.CommandOverride, imageEntrypoint, imageCmd)...)

	// Create and start container
	reportProgress(req, CreateStart, imageSize, 0, "Starting container")

	createCmd := m.execNerdctl(ctx, host, runArgs...)

	// Log the command for debugging
	slog.Info("Creating container", "command", createCmd.Args, "boxID", req.BoxID)

	// Debug: Log the exact command being run
	if len(createCmd.Args) >= 2 && createCmd.Args[0] == "ssh" {
		slog.Debug("SSH command", "host", createCmd.Args[1], "command", createCmd.Args[2])
	} else {
		slog.Debug("Direct command", "args", createCmd.Args)
	}

	// Use CombinedOutput to capture both stdout and stderr
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
	containerIP, err := m.waitForContainerRunning(ctx, host, containerID, networkName, cleanupContainerDir)
	if err != nil {
		return nil, err
	}

	// Configure SSH in the container (synchronously - block until ready)
	reportProgress(req, CreateSSH, imageSize, 0, "Configuring SSH")

	if !autoStartSSH {
		// Only setup SSH if not auto-started by the container
		sshCtx, sshCancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer sshCancel()

		if err := m.setupContainerSSH(sshCtx, containerID, host, req.Name, sshKeys); err != nil {
			// SSH setup failed - this is critical, so return an error
			// Clean up the container since it's not usable without SSH
			m.execNerdctl(ctx, host, "rm", "-f", containerID).Run()
			cleanupContainerDir()
			return nil, fmt.Errorf("failed to setup SSH in container: %w", err)
		}
	}

	// Mark creation as done - SSH is now ready
	reportProgress(req, CreateDone, imageSize, 0, "Container ready")

	// Default to root user if not specified in image
	if imageUser == "" {
		imageUser = "root"
	}

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
		SSHCAPublicKey:       sshKeys.CAPublicKey,
		SSHHostCertificate:   sshKeys.HostCertificate,
		SSHClientPrivateKey:  sshKeys.ClientPrivateKey,
		SSHPort:              sshPort,
		SSHUser:              imageUser,
	}

	// Set up SSH tunnel if we're using a remote host
	if host != "" && !strings.HasPrefix(host, "/") {
		if err := m.setupSSHTunnel(containerID, host, sshPort); err != nil {
			slog.Warn("Failed to set up SSH tunnel for container", "container", containerID, "error", err)
			// Don't fail container creation, just log the warning
		}
	}

	slog.Info("Created container", "id", containerID, "host", host, "ip", containerIP, "ssh_port", sshPort)

	return container, nil
}

// waitForContainerRunning waits for a container to reach "running" status and returns its IP address
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

// setupSSHTunnel sets up an SSH tunnel for accessing container SSH port from localhost
func (m *NerdctlManager) setupSSHTunnel(containerID, host string, sshPort int) error {
	// Parse SSH host
	sshHost := host
	sshHost = strings.TrimPrefix(sshHost, "ssh://")

	// Create SSH tunnel command: ssh -N -L localPort:localhost:remotePort user@host
	// -N: Don't execute remote command
	// -L: Local port forwarding
	// -o StrictHostKeyChecking=no: Skip host key checking (for dev mode)
	// -o UserKnownHostsFile=/dev/null: Don't save host key
	cmd := exec.Command("ssh",
		"-N",
		"-L", fmt.Sprintf("%d:localhost:%d", sshPort, sshPort),
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "ServerAliveInterval=30",
		"-o", "ServerAliveCountMax=3",
		sshHost,
	)

	// Start the SSH tunnel in background
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start SSH tunnel: %w", err)
	}

	// Store the tunnel command for cleanup
	m.mu.Lock()
	m.sshTunnels[containerID] = cmd
	m.mu.Unlock()

	slog.Info("Started SSH tunnel for container", "id", containerID, "local_port", sshPort, "remote_host", sshHost, "remote_port", sshPort)

	// Monitor the tunnel in a goroutine
	go func() {
		if err := cmd.Wait(); err != nil {
			// Only log if not killed intentionally
			if err.Error() != "signal: killed" {
				slog.Warn("SSH tunnel for container exited", "id", containerID, "error", err)
			}
		}
		// Clean up the tunnel from the map
		m.mu.Lock()
		delete(m.sshTunnels, containerID)
		m.mu.Unlock()
	}()

	return nil
}

// sshdCmd creates the sshd user (for privsev) if necessary then starts sshd.
const sshdCmd = "id sshd >/dev/null 2>&1" +
	"|| (groupadd -g 65534 nogroup 2>/dev/null || true; useradd -u 105 -g 65534 -c 'sshd privsep' -d /exe.dev/var/empty -s /usr/sbin/nologin sshd) 2>/dev/null" +
	"; exec /exe.dev/bin/sshd -f /exe.dev/etc/ssh/sshd_config"

// setupContainerSSH configures SSH inside the container
// This is used for containers that have an entrypoint already.
func (m *NerdctlManager) setupContainerSSH(ctx context.Context, containerID, host, containerName string, sshKeys *ContainerSSHKeys) error {
	slog.Info("Starting SSH setup for container", "id", containerID, "name", containerName)
	startCmd := m.execNerdctl(ctx, host, "exec", "-d", "-u", "root", containerID, "sh", "-c", sshdCmd)
	if output, err := startCmd.CombinedOutput(); err != nil {
		slog.Warn("SSH daemon start failed", "error", err, "output", string(output))
		// Don't return error - sshd might still be running
	} else {
		slog.Info("SSH daemon started successfully in container", "id", containerID)
	}

	// Spin-wait for sshd to fully daemonize and initialize
	var sshRunning bool
	for i := 0; i < 10; i++ {
		verifyCmd := m.execNerdctl(ctx, host, "exec", "-u", "root", containerID, "sh", "-c", "ps aux | grep -v grep | grep -E 'sshd.*listener'")
		output, err := verifyCmd.CombinedOutput()
		if err == nil && len(strings.TrimSpace(string(output))) > 0 {
			sshRunning = true
			slog.Info("SSH daemon verified running in container", "id", containerID, "attempt", i+1)
			slog.Debug("SSH process", "output", strings.TrimSpace(string(output)))
			break
		}
		if i == 5 {
			slog.Info("Still waiting for SSH daemon", "attempt", i+1, "total", 10)
		}
		time.Sleep(100 * time.Millisecond)
	}

	if !sshRunning {
		slog.Warn("SSH daemon process not detected in container after 1 second", "id", containerID)
	}

	return nil
}

// getHostArch gets the architecture of the host
func (m *NerdctlManager) getHostArch(ctx context.Context, host string) (string, error) {
	// Use SSH pool for better performance
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

	// Ensure SSH tunnel exists for remote containers when accessed
	if container.Status == StatusRunning && host != "" && !strings.HasPrefix(host, "/") && container.SSHPort > 0 {
		// Check if tunnel already exists
		m.mu.RLock()
		tunnelExists := m.sshTunnels[container.ID] != nil
		m.mu.RUnlock()

		if !tunnelExists {
			slog.Info("SSH tunnel not found for container, creating one", "id", container.ID, "port", container.SSHPort)
			if err := m.setupSSHTunnel(container.ID, host, container.SSHPort); err != nil {
				slog.Warn("Failed to set up SSH tunnel for container", "id", container.ID, "error", err)
			}
		}
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

	// Restart SSH tunnel if we're using a remote host
	host := container.DockerHost
	if host != "" && !strings.HasPrefix(host, "/") && container.SSHPort > 0 {
		// Check if tunnel already exists
		m.mu.RLock()
		tunnelExists := m.sshTunnels[container.ID] != nil
		m.mu.RUnlock()

		if !tunnelExists {
			if err := m.setupSSHTunnel(container.ID, host, container.SSHPort); err != nil {
				slog.Warn("Failed to set up SSH tunnel for container", "id", container.ID, "error", err)
			}
		}
	}

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

	// Kill any SSH tunnel for this container
	m.mu.Lock()
	if tunnel, exists := m.sshTunnels[container.ID]; exists {
		if err := tunnel.Process.Kill(); err != nil {
			slog.Warn("Failed to kill SSH tunnel for container", "id", container.ID, "error", err)
		}
		delete(m.sshTunnels, container.ID)
	}
	m.mu.Unlock()

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
		sourcePath := fmt.Sprintf("/data/exed/containers/box-%d", boxID)
		deletedPath := fmt.Sprintf("/data/exed/deleted/box-%d", boxID)

		// Check if source disk exists
		checkCmd := m.ExecSSHCommand(ctx, container.DockerHost, "test", "-d", sourcePath)
		if err := checkCmd.Run(); err == nil {
			// Source exists, proceed with move
			// First create the deleted directory if it doesn't exist
			mkdirCmd := m.ExecSSHCommand(ctx, container.DockerHost, "mkdir", "-p", "/data/exed/deleted")
			if err := mkdirCmd.Run(); err != nil {
				slog.Warn("Failed to create deleted directory", "error", err)
			}

			// Check if destination already exists (from a previous deletion)
			checkDestCmd := m.ExecSSHCommand(ctx, container.DockerHost, "test", "-d", deletedPath)
			if err := checkDestCmd.Run(); err == nil {
				// Destination exists, append timestamp to make it unique
				timestamp := time.Now().Format("20060102-150405")
				deletedPath = fmt.Sprintf("/data/exed/deleted/box-%d-%s", boxID, timestamp)
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

	// TODO: Clean up network if this was the last container in the allocation
	// For now, leave networks up to avoid disrupting other containers

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

func (m *NerdctlManager) listContainersWithFilter(ctx context.Context, filter, allocID string) ([]*Container, error) {
	// Determine which host to query
	host := ""
	if m.config != nil && len(m.config.ContainerdAddresses) > 0 {
		host = m.config.ContainerdAddresses[0]
	}

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

// ExecuteInContainer executes a command inside a container
func (m *NerdctlManager) ExecuteInContainer(ctx context.Context, allocID, containerID string, command []string, stdin io.Reader, stdout, stderr io.Writer) error {
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

// ResizeContainerTTY resizes the TTY of a container
func (m *NerdctlManager) ResizeContainerTTY(ctx context.Context, allocID, containerID string, width, height int) error {
	// nerdctl doesn't have a resize command, this would need to be implemented differently
	// For now, return nil
	return nil
}

// BuildImage builds a container image
func (m *NerdctlManager) BuildImage(ctx context.Context, req *BuildRequest) (*BuildResult, error) {
	// nerdctl supports building with buildkit
	// Implementation would go here
	return nil, fmt.Errorf("build not yet implemented for nerdctl")
}

// PauseContainer pauses a running container
func (m *NerdctlManager) PauseContainer(ctx context.Context, allocID, containerID string) error {
	container, err := m.GetContainer(ctx, allocID, containerID)
	if err != nil {
		return err
	}

	cmd := m.execNerdctl(ctx, container.DockerHost, "pause", container.ID)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to pause container: %w: %s", err, output)
	}

	container.Status = StatusPending
	return nil
}

// UnpauseContainer unpauses a paused container
func (m *NerdctlManager) UnpauseContainer(ctx context.Context, allocID, containerID string) error {
	container, err := m.GetContainer(ctx, allocID, containerID)
	if err != nil {
		return err
	}

	cmd := m.execNerdctl(ctx, container.DockerHost, "unpause", container.ID)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to unpause container: %w: %s", err, output)
	}

	container.Status = StatusRunning
	return nil
}

// ConnectToContainer establishes a connection to a container
func (m *NerdctlManager) ConnectToContainer(ctx context.Context, allocID, containerID string) (*ContainerConnection, error) {
	container, err := m.GetContainer(ctx, allocID, containerID)
	if err != nil {
		return nil, err
	}

	// TODO: Implement container connection for nerdctl
	_ = container
	return nil, fmt.Errorf("connect not yet implemented for nerdctl")
}

// GetBuildStatus gets the status of a build
func (m *NerdctlManager) GetBuildStatus(ctx context.Context, buildID string) (*BuildResult, error) {
	// TODO: Implement build status for nerdctl
	return nil, fmt.Errorf("build status not yet implemented for nerdctl")
}

// GetContainerDiagnostics retrieves diagnostic information for a container
func (m *NerdctlManager) GetContainerDiagnostics(ctx context.Context, allocID, containerName string) (string, error) {
	// Find container by name - query containerd
	containers, err := m.ListContainers(ctx, allocID)
	if err != nil {
		return "", fmt.Errorf("failed to list containers: %w", err)
	}

	var container *Container
	for _, c := range containers {
		if c.Name == containerName {
			container = c
			break
		}
	}

	if container == nil {
		return "", fmt.Errorf("container %s not found in allocation %s", containerName, allocID)
	}

	// Run diagnostic commands
	var diagnostics strings.Builder

	// Get container inspect data
	inspectCmd := m.execNerdctl(ctx, container.DockerHost, "inspect", container.ID)
	if output, err := inspectCmd.Output(); err == nil {
		diagnostics.WriteString("Container Inspect:\n")
		diagnostics.Write(output)
		diagnostics.WriteString("\n\n")
	}

	// Get container stats
	statsCmd := m.execNerdctl(ctx, container.DockerHost, "stats", "--no-stream", container.ID)
	if output, err := statsCmd.Output(); err == nil {
		diagnostics.WriteString("Container Stats:\n")
		diagnostics.Write(output)
		diagnostics.WriteString("\n\n")
	}

	// Get recent logs
	logsCmd := m.execNerdctl(ctx, container.DockerHost, "logs", "--tail", "50", container.ID)
	if output, err := logsCmd.Output(); err == nil {
		diagnostics.WriteString("Recent Logs:\n")
		diagnostics.Write(output)
	}

	return diagnostics.String(), nil
}

// Close cleans up the manager
func (m *NerdctlManager) Close() error {
	// Kill all SSH tunnels
	m.mu.Lock()
	for containerID, tunnel := range m.sshTunnels {
		if err := tunnel.Process.Kill(); err != nil {
			slog.Warn("Failed to kill SSH tunnel for container", "id", containerID, "error", err)
		}
	}
	m.sshTunnels = make(map[string]*exec.Cmd)
	m.mu.Unlock()

	// Close SSH connection pool
	m.sshPool.Close()

	return nil
}

// GetBackendType returns the backend type
func (m *NerdctlManager) GetBackendType() string {
	return "nerdctl"
}

// prepareRovolFS copies the embedded RovolFS files to the host for mounting into containers
func (m *NerdctlManager) prepareRovolFS(ctx context.Context, host string, boxID int) (string, error) {
	// Use cached host architecture (already mapped to Go arch names)
	// Get the RovolFS for the host architecture
	rovolFS, err := GetRovolFS(m.hostArch)
	if err != nil {
		return "", fmt.Errorf("failed to get RovolFS for %s: %w", m.hostArch, err)
	}

	// Use box ID for rovol directory path
	remoteDir := fmt.Sprintf("/data/exed/rovol/rovol-%d", boxID)

	// Check if rovol already exists for this box
	checkCmd := m.ExecSSHCommand(ctx, host, "test", "-d", remoteDir)
	if err := checkCmd.Run(); err == nil {
		// Rovol already exists for this box, reuse it
		slog.Info("Reusing existing RovolFS for box", "boxID", boxID, "dir", remoteDir)
		return remoteDir, nil
	}

	// Create the remote directory using the SSH pool
	mkdirCmd := m.sshPool.ExecCommand(ctx, host, "sudo", "mkdir", "-p", remoteDir)
	if output, err := mkdirCmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("failed to create remote directory: %w: %s", err, output)
	}

	// Create a temp directory to stage the rovol files locally
	tempDir, err := os.MkdirTemp("", "rovol-staging-*")
	if err != nil {
		return "", fmt.Errorf("failed to create temp directory: %w", err)
	}
	defer os.RemoveAll(tempDir)

	// Walk through the embedded filesystem and recreate it locally
	err = fs.WalkDir(rovolFS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if path == "." {
			return nil
		}

		localPath := filepath.Join(tempDir, path)

		if d.IsDir() {
			// Create directory locally
			if err := os.MkdirAll(localPath, 0o755); err != nil {
				return fmt.Errorf("failed to create local directory %s: %w", localPath, err)
			}
			return nil
		}

		// Read file content
		content, err := fs.ReadFile(rovolFS, path)
		if err != nil {
			return fmt.Errorf("failed to read file %s: %w", path, err)
		}

		// Write file locally with proper permissions
		mode := os.FileMode(0o644)
		if strings.Contains(path, "bin/") || strings.HasSuffix(path, ".so.1") {
			mode = 0o755
		}

		if err := os.WriteFile(localPath, content, mode); err != nil {
			return fmt.Errorf("failed to write file %s: %w", localPath, err)
		}

		return nil
	})
	if err != nil {
		return "", fmt.Errorf("failed to stage files locally: %w", err)
	}

	// Transfer the entire directory structure using the SSH pool's SCP method
	// The temp directory has a random suffix, so we need to get just the basename
	tempBaseName := filepath.Base(tempDir)
	tempRemotePath := filepath.Join("/tmp", tempBaseName)

	// First copy the temp directory to remote /tmp
	if err := m.sshPool.SCP(ctx, host, "/tmp", tempDir); err != nil {
		return "", fmt.Errorf("failed to transfer rovol files: %w", err)
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
		return "", fmt.Errorf("failed to move files to final location: %w: %s", err, output)
	}

	slog.Info("Successfully copied RovolFS files", "arch", m.hostArch, "dir", remoteDir)

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

	return remoteDir, nil
}

// VerifyDisk checks if a persistent disk exists for a given box ID
func (m *NerdctlManager) VerifyDisk(ctx context.Context, host string, boxID int) (bool, error) {
	diskPath := fmt.Sprintf("/data/exed/containers/box-%d", boxID)

	// Check if directory exists
	checkCmd := m.ExecSSHCommand(ctx, host, "test", "-d", diskPath)
	if err := checkCmd.Run(); err != nil {
		return false, nil // Directory doesn't exist
	}

	// Verify it has expected subdirectories (basic integrity check)
	verifyCmd := m.ExecSSHCommand(ctx, host, "test", "-d", filepath.Join(diskPath, "exe.dev"))
	if err := verifyCmd.Run(); err != nil {
		return false, nil // Directory exists but missing exe.dev subdirectory
	}

	return true, nil
}

// prepareContainerExeDev creates a container-specific /exe.dev directory with SSH keys
func (m *NerdctlManager) prepareContainerExeDev(ctx context.Context, host string, boxID int, sshKeys *ContainerSSHKeys) (string, error) {
	// Base directory for this container's files - use box ID for stable path
	containerDir := fmt.Sprintf("/data/exed/containers/box-%d/exe.dev", boxID)

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
		// First ensure the rovol files are prepared for this box
		rovolPath, err := m.prepareRovolFS(ctx, host, boxID)
		if err != nil {
			return "", fmt.Errorf("failed to prepare RovolFS for box %d: %w", boxID, err)
		}

		// Combine directory creation and CoW clone into a single command for speed
		// This reduces SSH round-trips significantly
		// Note: The source files should already be owned by root:root from prepareRovolFS
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

	slog.Info("Wrote all container-specific SSH files")

	slog.Info("Successfully prepared container-specific /exe.dev directory", "dir", containerDir)
	return containerDir, nil
}
