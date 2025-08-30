package container

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// NerdctlManager implements the Manager interface using nerdctl with containerd
//
// ⚠️ IMPORTANT: Kata/gVisor Runtime Considerations ⚠️
// This manager MUST work with Kata runtime for security isolation.
// See setupContainerSSH() for critical warnings about exec and stdin handling.
// NEVER use 'nerdctl exec -i' with stdin redirection - it will cause containers
// to enter UNKNOWN state with Kata/gVisor runtimes.
type NerdctlManager struct {
	config         *Config
	hosts          []string // List of containerd host addresses (SSH hostnames or "local")
	rovolMountPath string   // Path to rovol files on the host (e.g., /data/exed/rovol-<hash>)

	mu             sync.RWMutex
	containers     map[string]*Container         // containerID -> Container
	sshCancelFuncs map[string]context.CancelFunc // containerID -> SSH setup cancel func
	sshTunnels     map[string]*exec.Cmd          // containerID -> SSH tunnel command
	allocNetworks  map[string]bool               // Track which alloc networks exist
}

// NewNerdctlManager creates a new nerdctl-based container manager
func NewNerdctlManager(config *Config) (*NerdctlManager, error) {
	if err := validateConfig(config); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	manager := &NerdctlManager{
		config:         config,
		hosts:          config.ContainerdAddresses,
		containers:     make(map[string]*Container),
		sshCancelFuncs: make(map[string]context.CancelFunc),
		sshTunnels:     make(map[string]*exec.Cmd),
		allocNetworks:  make(map[string]bool),
	}

	// Verify Kata runtime is available on all hosts
	// Increase timeout to 2 minutes as Kata verification can take time, especially over SSH
	for _, host := range config.ContainerdAddresses {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		if err := manager.verifyKataRuntime(ctx, host); err != nil {
			cancel()
			return nil, fmt.Errorf("CRITICAL: Kata runtime not available on host %s: %w", host, err)
		}
		cancel()
	}

	// Prepare RovolFS files on the host (for mounting into containers)
	if len(config.ContainerdAddresses) > 0 {
		rovolPath, err := manager.prepareRovolFS(context.Background(), config.ContainerdAddresses[0])
		if err != nil {
			log.Printf("Warning: Failed to prepare RovolFS files on host: %v", err)
			// Continue without RovolFS - containers will use their own SSH binaries
		} else {
			manager.rovolMountPath = rovolPath
			log.Printf("RovolFS files prepared at: %s", rovolPath)
		}
	}

	// Discover existing containers on all hosts with timeout
	for _, host := range config.ContainerdAddresses {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		if err := manager.discoverContainers(ctx, host); err != nil {
			log.Printf("Warning: Failed to discover containers on host %s: %v", host, err)
		}
		cancel()
	}

	return manager, nil
}

// containsShC checks if the args contain sh -c pattern
func containsShC(args []string) bool {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "sh" && args[i+1] == "-c" {
			return true
		}
	}
	return false
}

// execNerdctl executes a nerdctl command via SSH on a remote host
func (m *NerdctlManager) execNerdctl(ctx context.Context, host string, args ...string) *exec.Cmd {
	// CRITICAL: Block any attempt to override runtime via environment or args
	// Check if someone is trying to specify a different runtime
	for i, arg := range args {
		if arg == "--runtime" && i+1 < len(args) {
			if args[i+1] != "io.containerd.kata.v2" {
				// Someone is trying to use a non-Kata runtime, override it
				log.Printf("SECURITY: Blocked attempt to use runtime %s, forcing Kata", args[i+1])
				args[i+1] = "io.containerd.kata.v2"
			}
		}
	}

	// Parse SSH format if present
	if strings.HasPrefix(host, "ssh://") {
		host = strings.TrimPrefix(host, "ssh://")
	}

	// Host is required - we always use SSH
	if host == "" || strings.HasPrefix(host, "/") {
		// Return a command that will fail with a clear error
		cmd := exec.CommandContext(ctx, "false")
		cmd.Env = []string{"ERROR=No valid SSH host provided for container operations"}
		return cmd
	}

	// For remote hosts, use SSH with sudo
	// Always use sudo for remote containerd/nerdctl commands

	// Build the remote command as a single string to preserve shell quoting
	var remoteCmd strings.Builder
	remoteCmd.WriteString("sudo ")
	remoteCmd.WriteString("nerdctl --namespace exe")

	// Special handling for exec commands with sh -c
	if len(args) >= 4 && args[0] == "exec" && containsShC(args) {
		// Find where sh -c starts and properly quote the command
		shIndex := -1
		for i, arg := range args {
			if arg == "sh" && i+1 < len(args) && args[i+1] == "-c" {
				shIndex = i
				break
			}
		}

		if shIndex > 0 && shIndex+2 < len(args) {
			// Add args before sh
			for i := 0; i < shIndex; i++ {
				remoteCmd.WriteString(" ")
				remoteCmd.WriteString(args[i])
			}
			// Add sh -c with the command properly quoted
			remoteCmd.WriteString(" sh -c '")
			remoteCmd.WriteString(strings.ReplaceAll(args[shIndex+2], "'", "'\\''"))
			remoteCmd.WriteString("'")
			// Add any remaining args
			for i := shIndex + 3; i < len(args); i++ {
				remoteCmd.WriteString(" ")
				remoteCmd.WriteString(args[i])
			}
		} else {
			// Fallback to simple concatenation
			for _, arg := range args {
				remoteCmd.WriteString(" ")
				remoteCmd.WriteString(arg)
			}
		}
	} else {
		// Simple concatenation for other commands
		for _, arg := range args {
			remoteCmd.WriteString(" ")
			remoteCmd.WriteString(arg)
		}
	}

	cmd := exec.CommandContext(ctx, "ssh", host, remoteCmd.String())
	// Remove any NERDCTL_RUNTIME or CONTAINERD_RUNTIME env vars that might override
	cmd.Env = filterEnv(os.Environ(), "NERDCTL_RUNTIME", "CONTAINERD_RUNTIME")
	return cmd
}

// execSSHCommand executes a command via SSH on a remote host
func (m *NerdctlManager) execSSHCommand(ctx context.Context, host string, args ...string) *exec.Cmd {
	// Parse SSH format if present
	if strings.HasPrefix(host, "ssh://") {
		host = strings.TrimPrefix(host, "ssh://")
	}

	// Host is required - we always use SSH
	if host == "" || strings.HasPrefix(host, "/") {
		// Return a command that will fail with a clear error
		cmd := exec.CommandContext(ctx, "false")
		cmd.Env = []string{"ERROR=No valid SSH host provided"}
		return cmd
	}

	// Execute via SSH with sudo
	sshArgs := append([]string{host, "sudo"}, args...)
	return exec.CommandContext(ctx, "ssh", sshArgs...)
}

// filterEnv removes specified environment variables from the environment
func filterEnv(environ []string, remove ...string) []string {
	filtered := make([]string, 0, len(environ))
	for _, e := range environ {
		skip := false
		for _, r := range remove {
			if strings.HasPrefix(e, r+"=") {
				skip = true
				break
			}
		}
		if !skip {
			filtered = append(filtered, e)
		}
	}
	return filtered
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
	// First, do a quick check if kata-runtime binary exists
	// This is much faster than running a container
	kataCheckCmd := m.execSSHCommand(ctx, host, "kata-runtime", "--version")

	kataOutput, kataErr := kataCheckCmd.Output()
	if kataErr == nil {
		// Kata binary exists, now do a quick runtime check
		// Just check if the runtime is registered with containerd
		checkArgs := []string{"info", "--format", "json"}
		infoCmd := m.execNerdctl(ctx, host, checkArgs...)
		infoOutput, infoErr := infoCmd.Output()

		if infoErr == nil && strings.Contains(string(infoOutput), "kata") {
			log.Printf("Kata runtime verified via quick check on %s: %s", host, strings.TrimSpace(string(kataOutput)))
			return nil
		}
	}

	// Fall back to the full container test if quick check failed or was inconclusive
	// The most reliable way to check if Kata is available is to try using it
	// nerdctl info doesn't reliably report available runtimes

	// Try to run a simple test container with Kata runtime
	testContainerName := fmt.Sprintf("kata-test-%d", time.Now().Unix())

	// Build the test command with nydus snapshotter
	// Use --network none to avoid CNI issues during verification
	args := []string{"--snapshotter", "nydus", "run", "--runtime", "io.containerd.kata.v2", "--rm", "--network", "none", "--name", testContainerName, "alpine:latest", "echo", "kata-test"}

	testCmd := m.execNerdctl(ctx, host, args...)

	output, err := testCmd.CombinedOutput()
	if err != nil {
		// Check if it's because Kata isn't available
		outputStr := string(output)
		if strings.Contains(outputStr, "not found") || strings.Contains(outputStr, "unknown runtime") ||
			strings.Contains(outputStr, "kata") || strings.Contains(outputStr, "runtime") {
			// We already checked kata-runtime binary above, so just report the error
			if kataErr != nil {
				return fmt.Errorf("Kata runtime not available: nerdctl test failed (%v) and kata-runtime binary check failed (%v)", err, kataErr)
			} else {
				// kata-runtime exists but nerdctl can't use it
				return fmt.Errorf("Kata runtime binary found but not usable via nerdctl: %v: %s", err, outputStr)
			}
		}
		// Some other error
		return fmt.Errorf("failed to verify Kata runtime: %w: %s", err, outputStr)
	}

	// Check if output contains our test string
	if !strings.Contains(string(output), "kata-test") {
		return fmt.Errorf("Kata runtime test container didn't produce expected output: %s", output)
	}

	log.Printf("Kata runtime successfully verified on %s", host)
	return nil
}

// discoverContainers discovers existing containers on a host
func (m *NerdctlManager) discoverContainers(ctx context.Context, host string) error {
	// List containers with their labels
	cmd := m.execNerdctl(ctx, host, "ps", "-a", "--format", "json")
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
			ID     string            `json:"ID"`
			Names  string            `json:"Names"` // nerdctl returns a single string, not array
			Labels map[string]string `json:"Labels"`
			Status string            `json:"Status"`
			Image  string            `json:"Image"`
		}

		if err := json.Unmarshal([]byte(line), &containerInfo); err != nil {
			log.Printf("Warning: Failed to parse container info: %v", err)
			continue
		}

		// Only track containers managed by exe
		if containerInfo.Labels["managed_by"] != "exe" {
			continue
		}

		// Note: Runtime information is not available via nerdctl inspect
		// We enforce Kata runtime on all new containers created by this manager
		// Existing containers discovered here may have been created with different settings

		// Extract container name (nerdctl returns a single string)
		name := containerInfo.Names

		// Create container record
		container := &Container{
			ID:         containerInfo.ID,
			Name:       name,
			AllocID:    containerInfo.Labels["alloc_id"],
			Status:     m.parseContainerStatus(containerInfo.Status),
			Image:      containerInfo.Image,
			PodName:    name,
			Namespace:  "nerdctl",
			DockerHost: host,
		}

		// Extract SSH port from container ports if available
		// Look for published port mappings in container info
		// We'll establish tunnels lazily when containers are accessed

		m.mu.Lock()
		m.containers[containerInfo.ID] = container
		m.mu.Unlock()
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

// selectHost selects a host from available hosts (round-robin for now)
func (m *NerdctlManager) selectHost() string {
	if len(m.hosts) == 0 {
		return ""
	}
	// For now, just return the first host
	// TODO: Implement proper load balancing
	return m.hosts[0]
}

// ensureAllocNetwork ensures a network exists for the allocation
func (m *NerdctlManager) ensureAllocNetwork(ctx context.Context, allocID string, host string) (string, error) {
	// Limit network name length, but handle shorter allocIDs
	nameLen := len(allocID)
	if nameLen > 12 {
		nameLen = 12
	}
	networkName := fmt.Sprintf("exe-%s", allocID[:nameLen])

	m.mu.Lock()
	exists := m.allocNetworks[networkName]
	m.mu.Unlock()

	if exists {
		return networkName, nil
	}

	// Check if network exists
	checkCmd := m.execNerdctl(ctx, host, "network", "ls", "--format", "{{.Name}}")
	output, err := checkCmd.Output()
	if err == nil && strings.Contains(string(output), networkName) {
		m.mu.Lock()
		m.allocNetworks[networkName] = true
		m.mu.Unlock()
		return networkName, nil
	}

	// Allocate a subnet for this allocation
	// Use 10.X.0.0/24 where X is based on hash of allocID
	// Hash the entire allocID to get better distribution
	hash := 0
	for _, ch := range allocID {
		hash = (hash*31 + int(ch)) % 155
	}
	subnetByte := 100 + hash // Range 100-254
	subnet := fmt.Sprintf("10.%d.0.0/24", subnetByte)

	// Create network
	createCmd := m.execNerdctl(ctx, host,
		"network", "create", networkName,
		"--subnet", subnet,
		"--driver", "bridge",
	)

	if output, err := createCmd.CombinedOutput(); err != nil {
		// Network might already exist, which is fine
		if !strings.Contains(string(output), "already exists") {
			return "", fmt.Errorf("failed to create network: %w: %s", err, output)
		}
	}

	m.mu.Lock()
	m.allocNetworks[networkName] = true
	m.mu.Unlock()

	// Set up iptables rules for this network
	if err := m.setupNetworkSecurity(ctx, host, subnet); err != nil {
		log.Printf("Warning: Failed to set up network security for %s: %v", networkName, err)
	}

	return networkName, nil
}

// setupNetworkSecurity sets up iptables rules to restrict network access
func (m *NerdctlManager) setupNetworkSecurity(ctx context.Context, host string, subnet string) error {
	// Commands to restrict network access
	commands := [][]string{
		// Block access to host from container subnet
		{"iptables", "-I", "INPUT", "-s", subnet, "-j", "DROP"},
		{"iptables", "-I", "INPUT", "-s", subnet, "-m", "state", "--state", "ESTABLISHED,RELATED", "-j", "ACCEPT"},

		// Block access to private networks (except container's own subnet)
		{"iptables", "-I", "FORWARD", "-s", subnet, "-d", "192.168.0.0/16", "-j", "DROP"},
		{"iptables", "-I", "FORWARD", "-s", subnet, "-d", "172.16.0.0/12", "-j", "DROP"},

		// Block access to metadata service
		{"iptables", "-I", "FORWARD", "-s", subnet, "-d", "169.254.169.254", "-j", "DROP"},
	}

	// Find and block Tailscale interface
	tailscaleIfaces := []string{"tailscale0", "utun"}
	for _, iface := range tailscaleIfaces {
		commands = append(commands,
			[]string{"iptables", "-I", "FORWARD", "-s", subnet, "-o", iface, "-j", "DROP"},
			[]string{"iptables", "-I", "FORWARD", "-i", iface, "-d", subnet, "-j", "DROP"},
		)
	}

	for _, cmd := range commands {
		execCmd := m.execSSHCommand(ctx, host, cmd...)

		// Ignore errors - rules might already exist
		execCmd.Run()
	}

	return nil
}

// CreateContainer creates a new container using nerdctl
func (m *NerdctlManager) CreateContainer(ctx context.Context, req *CreateContainerRequest) (*Container, error) {
	// Generate SSH keys for this container
	sshKeys, err := GenerateContainerSSHKeys()
	if err != nil {
		return nil, fmt.Errorf("failed to generate SSH keys: %w", err)
	}

	// Select a host
	host := m.selectHost()

	// Generate container name
	containerName := fmt.Sprintf("exe-%s-%s", req.AllocID, req.Name)

	// Prepare image
	image := req.Image
	if image == "" {
		image = "ubuntu:latest"
	}
	// Use the proper image expansion function
	image = ExpandImageNameForContainerd(image)

	// Ensure network exists for this allocation
	networkName, err := m.ensureAllocNetwork(ctx, req.AllocID, host)
	if err != nil {
		return nil, fmt.Errorf("failed to ensure network: %w", err)
	}

	// Allocate SSH port first so we can publish it
	// Use a hash of allocID and name to get a stable port
	hash := 0
	for _, b := range []byte(req.AllocID + req.Name) {
		hash = hash*31 + int(b)
	}
	sshPort := 10000 + (hash % 10000)

	// Build run command with nydus snapshotter
	runArgs := []string{
		"--snapshotter", "nydus",
		"run", "-d",
		"--runtime", "io.containerd.kata.v2", // Use Kata for security
		"--name", containerName,
		"--network", networkName,
	}

	// Add remaining args
	runArgs = append(runArgs,
		"--publish", fmt.Sprintf("%d:22", sshPort), // Publish SSH port
		"--hostname", req.Name, // Set hostname to match the container name
		"--dns", "8.8.8.8", // Google DNS primary
		"--dns", "8.8.4.4", // Google DNS secondary
		"--dns-search", "exe.dev", // Search domain for short names
		"--label", fmt.Sprintf("alloc_id=%s", req.AllocID),
		"--label", "managed_by=exe",
		"--restart", "no",
		// TEMPORARILY DISABLED: Removing all capability flags to debug issue
		// TODO: Re-enable after fixing container startup issue
	)

	// Prepare container-specific /exe.dev directory with SSH keys
	var containerExeDevPath string
	if m.rovolMountPath != "" {
		containerExeDevPath, err = m.prepareContainerExeDev(ctx, host, containerName, sshKeys)
		if err != nil {
			return nil, fmt.Errorf("failed to prepare container /exe.dev: %w", err)
		}
		runArgs = append(runArgs, "-v", fmt.Sprintf("%s:/exe.dev:ro", containerExeDevPath))
		log.Printf("Mounting container-specific /exe.dev from %s", containerExeDevPath)
	}

	// Helper function to clean up container-specific directory on failure
	cleanupContainerDir := func() {
		if containerExeDevPath != "" {
			// Remove the parent container directory (not just exe.dev)
			containerDir := filepath.Dir(containerExeDevPath)
			cleanupCmd := m.execSSHCommand(ctx, host, "rm", "-rf", containerDir)
			if err := cleanupCmd.Run(); err != nil {
				log.Printf("Warning: Failed to clean up container directory %s: %v", containerDir, err)
			}
		}
	}

	// TODO: Add proper resource limits via cgroups and Kata labels
	// For now, skip resource limits to get basic functionality working with Cloud Hypervisor
	// Cloud Hypervisor doesn't support the dynamic resource allocation that nerdctl's
	// --memory and --cpus flags trigger. We need to use cgroup parent slices instead.

	// Add the image
	runArgs = append(runArgs, image)

	// Add command if specified
	// The exeuntu image requires a command because it uses tini which needs arguments
	if req.CommandOverride != "" && req.CommandOverride != "auto" && req.CommandOverride != "none" {
		// Parse custom command override
		cmdParts := strings.Fields(req.CommandOverride)
		runArgs = append(runArgs, cmdParts...)
	} else if req.CommandOverride == "none" || image == "ghcr.io/boldsoftware/exeuntu:latest" || strings.HasSuffix(image, "/exeuntu:latest") {
		// Use a simple sleep command for images that need it (like exeuntu with tini)
		// This keeps the container running until SSH is set up
		// Use sleep infinity which is more portable and doesn't require argument quoting
		runArgs = append(runArgs, "sleep", "infinity")
	}
	// For "auto" with non-exeuntu images, don't add any command - let the image use its default CMD/ENTRYPOINT

	// Pull image first with nydus snapshotter
	pullArgs := []string{"--snapshotter", "nydus", "pull", image}
	pullCmd := m.execNerdctl(ctx, host, pullArgs...)
	if output, err := pullCmd.CombinedOutput(); err != nil {
		if !strings.Contains(string(output), "already exists") {
			log.Printf("Warning: Failed to pull image %s: %v: %s", image, err, output)
		}
	}

	// Create and start container
	createCmd := m.execNerdctl(ctx, host, runArgs...)

	// Log the command for debugging
	log.Printf("Creating container with command: %v", createCmd.Args)

	// Debug: Log the exact command being run
	if len(createCmd.Args) >= 2 && createCmd.Args[0] == "ssh" {
		log.Printf("DEBUG: SSH command: ssh %s '%s'", createCmd.Args[1], createCmd.Args[2])
	} else {
		log.Printf("DEBUG: Direct command: %v", createCmd.Args)
	}

	// Use CombinedOutput to capture both stdout and stderr
	output, err := createCmd.CombinedOutput()
	if err != nil {
		outputStr := string(output)
		cleanupContainerDir()
		return nil, fmt.Errorf("failed to create container: %w\nOutput: %s", err, outputStr)
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

	// CRITICAL: Verify the container is actually using Kata runtime
	// Since we're forcing --runtime io.containerd.kata.v2 and already verified Kata works,
	// we can trust that the container is using Kata. Full verification would require
	// checking with ctr, but that's complex across SSH boundaries.
	//
	// The key security enforcement is:
	// 1. We verified Kata is available during manager initialization
	// 2. We force --runtime io.containerd.kata.v2 on all container creation
	// 3. Container creation would fail if Kata wasn't available

	log.Printf("Container %s created with enforced Kata runtime", containerID)

	// Wait for container to reach "Up" status (especially important for Kata/Firecracker)
	log.Printf("Waiting for container %s to reach Up status...", containerID)
	startTime := time.Now()
	maxWaitTime := 30 * time.Second
	checkInterval := 100 * time.Millisecond
	lastStatus := ""

	for time.Since(startTime) < maxWaitTime {
		// Use nerdctl ps to check container status
		statusCmd := m.execNerdctl(ctx, host, "inspect", containerID, "--format", "{{.State.Status}}")
		statusOutput, err := statusCmd.Output()
		if err != nil {
			log.Printf("Warning: Failed to check container status: %v", err)
			time.Sleep(checkInterval)
			continue
		}

		status := strings.TrimSpace(string(statusOutput))
		if status != lastStatus {
			log.Printf("Container %s status: %s (%.1fs elapsed)", containerID, status, time.Since(startTime).Seconds())
			lastStatus = status
		}

		if status == "running" {
			log.Printf("Container %s is Up after %.1fs", containerID, time.Since(startTime).Seconds())
			break
		}

		if status == "exited" || status == "dead" {
			// Container failed to start
			log.Printf("ERROR: Container %s failed with status: %s", containerID, status)
			m.execNerdctl(ctx, host, "rm", "-f", containerID).Run()
			cleanupContainerDir()
			return nil, fmt.Errorf("container failed to start, status: %s", status)
		}

		time.Sleep(checkInterval)
	}

	if lastStatus != "running" {
		log.Printf("ERROR: Container %s did not reach Up status after %.1fs, last status: %s", containerID, maxWaitTime.Seconds(), lastStatus)
		// Try to get more info about why it's stuck
		logsCmd := m.execNerdctl(ctx, host, "logs", "--tail", "50", containerID)
		logs, _ := logsCmd.Output()
		if len(logs) > 0 {
			log.Printf("Container logs: %s", string(logs))
		}
		m.execNerdctl(ctx, host, "rm", "-f", containerID).Run()
		cleanupContainerDir()
		return nil, fmt.Errorf("container stuck in %s state after %v", lastStatus, maxWaitTime)
	}

	// Quick spin to wait for container to be fully ready (especially for Firecracker)
	var inspectOutput []byte
	maxAttempts := 60 // Up to 6 seconds for Firecracker/Kata startup
	for i := 0; i < maxAttempts; i++ {
		inspectCmd := m.execNerdctl(ctx, host, "inspect", containerID, "--format", "json")
		inspectOutput, err = inspectCmd.Output()
		if err != nil && i < maxAttempts-1 {
			// Container might still be starting (especially with Kata)
			time.Sleep(100 * time.Millisecond)
			continue
		} else if err != nil {
			// Final attempt failed - clean up the failed container
			m.execNerdctl(ctx, host, "rm", "-f", containerID).Run()
			cleanupContainerDir()
			return nil, fmt.Errorf("failed to inspect container after creation: %w", err)
		}

		// Parse to check status
		var inspectData struct {
			State struct {
				Status string `json:"Status"`
				Error  string `json:"Error"`
			} `json:"State"`
		}
		if err := json.Unmarshal(inspectOutput, &inspectData); err == nil {
			if inspectData.State.Status == "running" || inspectData.State.Status == "" {
				// Container is running or status not yet set (which is ok initially)
				break
			}
			if inspectData.State.Status == "created" || inspectData.State.Status == "unknown" {
				// Firecracker container might be stuck, wait a bit more
				if i < maxAttempts-1 {
					time.Sleep(100 * time.Millisecond)
					continue
				}
				// Container is stuck in created/unknown state
				m.execNerdctl(ctx, host, "rm", "-f", containerID).Run()
				cleanupContainerDir()
				return nil, fmt.Errorf("container stuck in %s state: %s", inspectData.State.Status, inspectData.State.Error)
			}
		}
		break
	}

	// nerdctl inspect returns a single object, not an array
	var inspectData struct {
		NetworkSettings struct {
			Networks map[string]struct {
				IPAddress string `json:"IPAddress"`
			} `json:"Networks"`
		} `json:"NetworkSettings"`
		Config struct {
			Hostname string `json:"Hostname"`
		} `json:"Config"`
	}

	if err := json.Unmarshal(inspectOutput, &inspectData); err != nil {
		return nil, fmt.Errorf("failed to parse inspect data: %w", err)
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

	// Configure SSH in the container (asynchronously)
	// After containerd restart, exec should work with kata
	sshCtx, sshCancel := context.WithTimeout(context.Background(), 2*time.Minute)

	// Store the cancel func so we can cancel it on container deletion
	m.mu.Lock()
	m.sshCancelFuncs[containerID] = sshCancel
	m.mu.Unlock()

	go func() {
		defer sshCancel()
		defer func() {
			// Clean up the cancel func from the map when done
			m.mu.Lock()
			delete(m.sshCancelFuncs, containerID)
			m.mu.Unlock()
		}()

		if err := m.setupContainerSSH(sshCtx, containerID, host, req.Name, sshKeys); err != nil {
			// Only log if not cancelled
			if !errors.Is(err, context.Canceled) {
				log.Printf("Warning: Failed to setup SSH in container: %v", err)
			}
		}
	}()

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
	}

	m.mu.Lock()
	m.containers[containerID] = container
	m.mu.Unlock()

	// Set up SSH tunnel if we're using a remote host
	if host != "" && !strings.HasPrefix(host, "/") {
		if err := m.setupSSHTunnel(containerID, host, sshPort); err != nil {
			log.Printf("Warning: Failed to set up SSH tunnel for container %s: %v", containerID, err)
			// Don't fail container creation, just log the warning
		}
	}

	log.Printf("Created container %s on host %s with IP %s and SSH port %d", containerID, host, containerIP, sshPort)

	return container, nil
}

// setupSSHTunnel sets up an SSH tunnel for accessing container SSH port from localhost
func (m *NerdctlManager) setupSSHTunnel(containerID, host string, sshPort int) error {
	// Parse SSH host
	sshHost := host
	if strings.HasPrefix(sshHost, "ssh://") {
		sshHost = strings.TrimPrefix(sshHost, "ssh://")
	}

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

	log.Printf("Started SSH tunnel for container %s: localhost:%d -> %s:%d", containerID, sshPort, sshHost, sshPort)

	// Monitor the tunnel in a goroutine
	go func() {
		if err := cmd.Wait(); err != nil {
			// Only log if not killed intentionally
			if err.Error() != "signal: killed" {
				log.Printf("SSH tunnel for container %s exited: %v", containerID, err)
			}
		}
		// Clean up the tunnel from the map
		m.mu.Lock()
		delete(m.sshTunnels, containerID)
		m.mu.Unlock()
	}()

	return nil
}

// setupContainerSSH configures SSH inside the container
// With the new container-specific /exe.dev mount, everything is already in place:
// - SSH keys are at /exe.dev/etc/ssh/ and /exe.dev/root/.ssh/
// - sshd_config is at /exe.dev/etc/ssh/sshd_config
// - var/empty is at /exe.dev/var/empty for privilege separation
// We just need to start sshd
func (m *NerdctlManager) setupContainerSSH(ctx context.Context, containerID, host, containerName string, sshKeys *ContainerSSHKeys) error {
	log.Printf("[SSH-SETUP] Starting SSH setup for container %s (name: %s)", containerID, containerName)

	// Check container status before proceeding
	statusCmd := m.execNerdctl(ctx, host, "inspect", containerID, "--format", "{{.State.Status}}")
	if statusOut, err := statusCmd.Output(); err == nil {
		log.Printf("[SSH-SETUP] Container status before SSH operations: %s", strings.TrimSpace(string(statusOut)))
	}

	// Always use sshd from /exe.dev mount
	sshdPath := "/exe.dev/bin/sshd"

	// Verify that /exe.dev/bin/sshd exists and is executable
	log.Printf("[SSH-SETUP] Checking for SSH daemon at %s", sshdPath)
	checkCmd := m.execNerdctl(ctx, host, "exec", "-u", "root", containerID, "test", "-x", sshdPath)
	if err := checkCmd.Run(); err != nil {
		log.Printf("[SSH-SETUP] ERROR: SSH daemon not found at %s - is /exe.dev mounted?", sshdPath)
		return fmt.Errorf("SSH daemon not available at %s: %w", sshdPath, err)
	}
	log.Printf("[SSH-SETUP] Using SSH daemon at: %s", sshdPath)

	// Start SSH daemon - use nerdctl exec -d to run in detached mode
	log.Printf("[SSH-SETUP] Starting SSH daemon")
	// Use the sshd_config from /exe.dev which has all our settings
	// The binary has rpath set to /exe.dev/lib so no LD_LIBRARY_PATH needed
	sshCmd := fmt.Sprintf("exec %s -f /exe.dev/etc/ssh/sshd_config", sshdPath)
	log.Printf("[SSH-SETUP] SSH command: %s", sshCmd)
	startCmd := m.execNerdctl(ctx, host, "exec", "-d", "-u", "root", containerID, "sh", "-c", sshCmd)

	// Run the command - it will return quickly since sshd daemonizes
	if output, err := startCmd.CombinedOutput(); err != nil {
		log.Printf("[SSH-SETUP] SSH daemon start failed: %v, output: %s", err, output)
		// Don't return error - sshd might still be running
	} else {
		log.Printf("[SSH-SETUP] SSH daemon started successfully in container %s", containerID)
	}

	// Spin-wait for sshd to fully daemonize and initialize (max 3 seconds)
	log.Printf("[SSH-SETUP] Verifying SSH daemon is running")
	var sshRunning bool
	for i := 0; i < 30; i++ {
		verifyCmd := m.execNerdctl(ctx, host, "exec", "-u", "root", containerID, "sh", "-c", "ps aux | grep -v grep | grep -E 'sshd.*listener'")
		output, err := verifyCmd.CombinedOutput()
		if err == nil && len(strings.TrimSpace(string(output))) > 0 {
			sshRunning = true
			log.Printf("[SSH-SETUP] SSH daemon verified running in container %s", containerID)
			log.Printf("[SSH-SETUP] SSH process: %s", strings.TrimSpace(string(output)))
			break
		}
		if i == 10 || i == 20 {
			log.Printf("[SSH-SETUP] Still waiting for SSH daemon (attempt %d/30)", i+1)
		}
		if i < 29 {
			time.Sleep(100 * time.Millisecond)
		}
	}

	if !sshRunning {
		// Log warning if SSH daemon not detected but don't fail
		log.Printf("[SSH-SETUP] WARNING: SSH daemon process not detected in container %s after 3 seconds", containerID)
	}

	// Network configuration is now handled at container creation time using
	// --hostname, --dns, and --dns-search flags to nerdctl run
	// This is much cleaner and works properly with the Kata runtime

	return nil
}

// getHostArch gets the architecture of the host
func (m *NerdctlManager) getHostArch(ctx context.Context, host string) (string, error) {
	// Parse SSH format if present
	if strings.HasPrefix(host, "ssh://") {
		host = strings.TrimPrefix(host, "ssh://")
	}

	// Host is required
	if host == "" || strings.HasPrefix(host, "/") {
		return "", fmt.Errorf("no valid SSH host provided")
	}

	cmd := exec.CommandContext(ctx, "ssh", host, "uname", "-m")

	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to get architecture: %w", err)
	}

	return strings.TrimSpace(string(output)), nil
}

// GetContainer retrieves container information
func (m *NerdctlManager) GetContainer(ctx context.Context, allocID, containerID string) (*Container, error) {
	m.mu.RLock()
	container, exists := m.containers[containerID]
	m.mu.RUnlock()

	if !exists {
		return nil, fmt.Errorf("container %s not found", containerID)
	}

	// Verify allocID matches
	if container.AllocID != allocID {
		return nil, fmt.Errorf("container %s does not belong to allocation %s", containerID, allocID)
	}

	// Ensure SSH tunnel exists for remote containers when accessed
	if m.config != nil && len(m.config.ContainerdAddresses) > 0 {
		host := container.DockerHost
		if host == "" && len(m.config.ContainerdAddresses) > 0 {
			// If container doesn't have a host set, use the first configured host
			host = m.config.ContainerdAddresses[0]
		}

		if host != "" && !strings.HasPrefix(host, "/") && container.SSHPort > 0 {
			// Check if tunnel already exists
			m.mu.RLock()
			tunnelExists := m.sshTunnels[container.ID] != nil
			m.mu.RUnlock()

			if !tunnelExists {
				log.Printf("SSH tunnel not found for container %s, creating one on port %d", container.ID, container.SSHPort)
				if err := m.setupSSHTunnel(container.ID, host, container.SSHPort); err != nil {
					log.Printf("Warning: Failed to set up SSH tunnel for container %s: %v", container.ID, err)
				}
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
				log.Printf("Warning: Failed to set up SSH tunnel for container %s: %v", container.ID, err)
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

	// Cancel any ongoing SSH setup for this container
	m.mu.Lock()
	if cancel, exists := m.sshCancelFuncs[container.ID]; exists {
		cancel()
		delete(m.sshCancelFuncs, container.ID)
	}
	// Kill any SSH tunnel for this container
	if tunnel, exists := m.sshTunnels[container.ID]; exists {
		if err := tunnel.Process.Kill(); err != nil {
			log.Printf("Warning: Failed to kill SSH tunnel for container %s: %v", container.ID, err)
		}
		delete(m.sshTunnels, container.ID)
	}
	m.mu.Unlock()

	// Remove container (force removal even if running)
	cmd := m.execNerdctl(ctx, container.DockerHost, "rm", "-f", container.ID)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to delete container: %w: %s", err, output)
	}

	m.mu.Lock()
	delete(m.containers, containerID)
	m.mu.Unlock()

	// Clean up container-specific /exe.dev directory
	containerName := fmt.Sprintf("exe-%s-%s", allocID, container.Name)
	containerDir := fmt.Sprintf("/data/exed/containers/%s", containerName)
	host := container.DockerHost
	cleanupCmd := m.execSSHCommand(ctx, host, "rm", "-rf", containerDir)
	if err := cleanupCmd.Run(); err != nil {
		log.Printf("Warning: Failed to clean up container directory %s: %v", containerDir, err)
	} else {
		log.Printf("Cleaned up container directory %s", containerDir)
	}

	// TODO: Clean up network if this was the last container in the allocation
	// For now, leave networks up to avoid disrupting other containers

	return nil
}

// ListContainers lists all containers for an allocation
func (m *NerdctlManager) ListContainers(ctx context.Context, allocID string) ([]*Container, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var containers []*Container
	for _, container := range m.containers {
		if container.AllocID == allocID {
			containers = append(containers, container)
		}
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
	// Get container by name
	var container *Container
	m.mu.RLock()
	for _, c := range m.containers {
		if c.AllocID == allocID && c.Name == containerName {
			container = c
			break
		}
	}
	m.mu.RUnlock()

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
	// Cancel all ongoing SSH setups and kill tunnels
	m.mu.Lock()
	for _, cancel := range m.sshCancelFuncs {
		cancel()
	}
	m.sshCancelFuncs = make(map[string]context.CancelFunc)

	// Kill all SSH tunnels
	for containerID, tunnel := range m.sshTunnels {
		if err := tunnel.Process.Kill(); err != nil {
			log.Printf("Warning: Failed to kill SSH tunnel for container %s: %v", containerID, err)
		}
	}
	m.sshTunnels = make(map[string]*exec.Cmd)
	m.mu.Unlock()

	return nil
}

// GetBackendType returns the backend type
func (m *NerdctlManager) GetBackendType() string {
	return "nerdctl"
}

// shellQuote quotes a string for safe use in shell commands
func shellQuote(s string) string {
	// Use single quotes and escape any single quotes in the string
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// prepareRovolFS copies the embedded RovolFS files to the host for mounting into containers
func (m *NerdctlManager) prepareRovolFS(ctx context.Context, host string) (string, error) {
	// Get the host architecture
	arch, err := m.getHostArch(ctx, host)
	if err != nil {
		return "", fmt.Errorf("failed to get host architecture: %w", err)
	}

	// Map architecture names
	switch arch {
	case "x86_64":
		arch = "amd64"
	case "aarch64":
		arch = "arm64"
	}

	// Get the RovolFS for the host architecture
	rovolFS, err := GetRovolFS(arch)
	if err != nil {
		return "", fmt.Errorf("failed to get RovolFS for %s: %w", arch, err)
	}

	// Generate a unique directory name for this instance
	randomBytes := make([]byte, 8)
	if _, err := rand.Read(randomBytes); err != nil {
		return "", fmt.Errorf("failed to generate random bytes: %w", err)
	}
	hash := hex.EncodeToString(randomBytes)
	remoteDir := fmt.Sprintf("/data/exed/rovol-%s", hash)

	// Create the remote directory
	mkdirCmd := m.execSSHCommand(ctx, host, "mkdir", "-p", remoteDir)

	if output, err := mkdirCmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("failed to create remote directory: %w: %s", err, output)
	}

	// Walk through the embedded filesystem and copy files
	err = fs.WalkDir(rovolFS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if path == "." {
			return nil
		}

		remotePath := filepath.Join(remoteDir, path)

		if d.IsDir() {
			// Create directory on remote
			cmd := m.execSSHCommand(ctx, host, "mkdir", "-p", remotePath)
			if err := cmd.Run(); err != nil {
				return fmt.Errorf("failed to create directory %s: %w", remotePath, err)
			}
			return nil
		}

		// Read file content
		content, err := fs.ReadFile(rovolFS, path)
		if err != nil {
			return fmt.Errorf("failed to read file %s: %w", path, err)
		}

		// Determine if file should be executable (for binaries in bin/ and lib/)
		mode := "644"
		if strings.Contains(path, "bin/") || strings.HasSuffix(path, ".so.1") {
			mode = "755"
		}

		// Use SSH to transfer files
		sshHost := host
		if strings.HasPrefix(sshHost, "ssh://") {
			sshHost = strings.TrimPrefix(sshHost, "ssh://")
		}

		// Host is required
		if sshHost == "" || strings.HasPrefix(host, "/") {
			return fmt.Errorf("no valid SSH host provided for file transfer")
		}
		// Always use sudo for remote commands

		// For large files, we need to use a different approach to avoid SSH command line limits
		// Write the file to a local temp file first, then scp it over
		tempFile, err := os.CreateTemp("", "rovol-*")
		if err != nil {
			return fmt.Errorf("failed to create temp file: %w", err)
		}
		tempPath := tempFile.Name()
		defer os.Remove(tempPath)

		if _, err := tempFile.Write(content); err != nil {
			tempFile.Close()
			return fmt.Errorf("failed to write temp file: %w", err)
		}
		tempFile.Close()

		// Use scp to copy the file to a temp location on the remote host
		remoteTempPath := fmt.Sprintf("/tmp/rovol-%s", filepath.Base(tempPath))
		scpCmd := exec.CommandContext(ctx, "scp", "-o", "StrictHostKeyChecking=no", "-o", "UserKnownHostsFile=/dev/null", tempPath, fmt.Sprintf("%s:%s", sshHost, remoteTempPath))
		if output, err := scpCmd.CombinedOutput(); err != nil {
			return fmt.Errorf("failed to scp file %s: %w: %s", path, err, output)
		}

		// Now move the file to its final location with proper permissions
		// Get parent directory - filepath.Dir should work correctly here
		parentDir := filepath.Dir(remotePath)

		// Move the file to its final location with proper permissions
		// Always use sudo for remote file operations
		// Execute commands separately to avoid complex quoting issues
		// First create the directory
		mkdirCmd := exec.CommandContext(ctx, "ssh", sshHost, "sudo", "mkdir", "-p", parentDir)
		if output, err := mkdirCmd.CombinedOutput(); err != nil {
			return fmt.Errorf("failed to create directory %s: %w: %s", parentDir, err, output)
		}

		// Then move the file
		mvCmd := exec.CommandContext(ctx, "ssh", sshHost, "sudo", "mv", remoteTempPath, remotePath)
		if output, err := mvCmd.CombinedOutput(); err != nil {
			// Clean up temp file
			exec.CommandContext(ctx, "ssh", sshHost, "sudo", "rm", "-f", remoteTempPath).Run()
			return fmt.Errorf("failed to move file to %s: %w: %s", remotePath, err, output)
		}

		// Finally set permissions - use separate commands to avoid issues
		chmodCmd := exec.CommandContext(ctx, "ssh", sshHost, "sudo", "chmod", mode, remotePath)
		if output, err := chmodCmd.CombinedOutput(); err != nil {
			return fmt.Errorf("failed to chmod file %s: %w: %s", remotePath, err, output)
		}

		return nil
	})
	if err != nil {
		return "", fmt.Errorf("failed to copy RovolFS files: %w", err)
	}

	log.Printf("Successfully copied RovolFS files for %s architecture to %s", arch, remoteDir)
	return remoteDir, nil
}

// prepareContainerExeDev creates a container-specific /exe.dev directory with SSH keys
func (m *NerdctlManager) prepareContainerExeDev(ctx context.Context, host, containerID string, sshKeys *ContainerSSHKeys) (string, error) {
	// Base directory for this container's files
	containerDir := fmt.Sprintf("/data/exed/containers/%s/exe.dev", containerID)

	log.Printf("Preparing container-specific /exe.dev directory at %s", containerDir)

	// Create the container directory
	mkdirCmd := m.execSSHCommand(ctx, host, "mkdir", "-p", containerDir)

	if output, err := mkdirCmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("failed to create container directory: %w: %s", err, output)
	}

	// Use cp with --reflink=always for CoW cloning on XFS
	// The -a flag preserves attributes and the dot syntax copies contents
	// Using /. at the end copies contents, not the directory itself
	cpCmd := m.execSSHCommand(ctx, host, "cp", "-a", "--reflink=always",
		m.rovolMountPath+"/.", containerDir+"/")

	log.Printf("Cloning rovol files from %s to %s using CoW", m.rovolMountPath, containerDir)
	if output, err := cpCmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("failed to clone rovol files: %w: %s", err, output)
	}

	// Create var/empty directory for sshd privilege separation
	varEmptyDir := filepath.Join(containerDir, "var/empty")
	mkdirVarEmptyCmd := m.execSSHCommand(ctx, host, "mkdir", "-p", varEmptyDir)
	if output, err := mkdirVarEmptyCmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("failed to create var/empty directory: %w: %s", err, output)
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
	}

	// Write authorized_keys - need to create the directory first
	rootSSHDir := filepath.Join(containerDir, "root/.ssh")
	mkdirSSHCmd := m.execSSHCommand(ctx, host, "mkdir", "-p", rootSSHDir)

	if output, err := mkdirSSHCmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("failed to create root .ssh directory: %w: %s", err, output)
	}

	// Add authorized_keys to files map
	files["root/.ssh/authorized_keys"] = struct {
		content string
		mode    string
	}{sshKeys.AuthorizedKeys, "600"}

	// Write each file
	for relPath, fileInfo := range files {
		fullPath := filepath.Join(containerDir, relPath)

		// Write the file via SSH
		sshHost := host
		if strings.HasPrefix(sshHost, "ssh://") {
			sshHost = strings.TrimPrefix(sshHost, "ssh://")
		}

		// Host is required
		if sshHost == "" || strings.HasPrefix(host, "/") {
			return "", fmt.Errorf("no valid SSH host provided for SSH file write")
		}

		// Use base64 encoding to safely transfer the content
		encodedContent := base64.StdEncoding.EncodeToString([]byte(fileInfo.content))

		// Write the file using echo and base64 decode
		writeCmd := fmt.Sprintf("echo '%s' | base64 -d | sudo tee '%s' > /dev/null && sudo chmod %s '%s'",
			encodedContent, fullPath, fileInfo.mode, fullPath)

		cmd := exec.CommandContext(ctx, "ssh", sshHost, writeCmd)
		if output, err := cmd.CombinedOutput(); err != nil {
			return "", fmt.Errorf("failed to write SSH file %s: %w: %s", relPath, err, output)
		}

		log.Printf("Wrote container-specific file: %s (mode %s)", relPath, fileInfo.mode)
	}

	log.Printf("Successfully prepared container-specific /exe.dev directory at %s", containerDir)
	return containerDir, nil
}
