package container

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// NetworkManager manages per-alloc network namespaces and subnets
type NetworkManager struct {
	mu sync.RWMutex
	// Map of allocID to NetworkInfo
	networks map[string]*NetworkInfo
	// Track allocated subnets to avoid conflicts
	allocatedSubnets map[string]bool
}

// NetworkInfo contains network configuration for an allocation
type NetworkInfo struct {
	AllocID       string
	BridgeName    string
	Subnet        *net.IPNet
	Gateway       net.IP
	NamespacePath string
	VethHost      string // Host side of veth pair
	VethNS        string // Namespace side of veth pair
	// Track containers in this network
	Containers map[string]*ContainerNetworkInfo
}

// ContainerNetworkInfo contains network details for a container
type ContainerNetworkInfo struct {
	ContainerID string
	IPAddress   net.IP
	SSHPort     int // Host port for SSH forwarding
}

// NewNetworkManager creates a new network manager
func NewNetworkManager() *NetworkManager {
	return &NetworkManager{
		networks:         make(map[string]*NetworkInfo),
		allocatedSubnets: make(map[string]bool),
	}
}

// execSSHCommand executes a command via SSH on a remote host
func (m *NetworkManager) execSSHCommand(ctx context.Context, host string, args ...string) *exec.Cmd {
	// Parse SSH format if present
	if strings.HasPrefix(host, "ssh://") {
		host = strings.TrimPrefix(host, "ssh://")
	}

	// Host is required - we always use SSH
	if host == "" || strings.HasPrefix(host, "/") {
		// Return a command that will fail with a clear error
		cmd := exec.CommandContext(ctx, "false")
		cmd.Env = []string{"ERROR=No valid SSH host provided for network operations"}
		return cmd
	}

	// Execute via SSH with sudo
	sshArgs := append([]string{host, "sudo"}, args...)
	return exec.CommandContext(ctx, "ssh", sshArgs...)
}

// CreateAllocNetwork creates a network namespace and bridge for an allocation
func (m *NetworkManager) CreateAllocNetwork(ctx context.Context, allocID string, host string) (*NetworkInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check if network already exists
	if info, exists := m.networks[allocID]; exists {
		return info, nil
	}

	// Allocate a unique subnet for this allocation
	subnet, gateway, err := m.allocateSubnet()
	if err != nil {
		return nil, fmt.Errorf("failed to allocate subnet: %w", err)
	}

	// Create network namespace
	nsName := fmt.Sprintf("exe-ns-%s", allocID)
	bridgeName := fmt.Sprintf("exe-br-%s", allocID[:8]) // Limit bridge name length
	vethHost := fmt.Sprintf("veth-h-%s", allocID[:8])
	vethNS := fmt.Sprintf("veth-n-%s", allocID[:8])

	info := &NetworkInfo{
		AllocID:       allocID,
		BridgeName:    bridgeName,
		Subnet:        subnet,
		Gateway:       gateway,
		NamespacePath: fmt.Sprintf("/var/run/netns/%s", nsName),
		VethHost:      vethHost,
		VethNS:        vethNS,
		Containers:    make(map[string]*ContainerNetworkInfo),
	}

	// Execute network setup commands
	if err := m.setupAllocNetwork(ctx, info, host); err != nil {
		// Clean up on failure
		delete(m.allocatedSubnets, subnet.String())
		return nil, fmt.Errorf("failed to setup network: %w", err)
	}

	m.networks[allocID] = info
	return info, nil
}

// allocateSubnet finds an available subnet for an allocation
func (m *NetworkManager) allocateSubnet() (*net.IPNet, net.IP, error) {
	// Use 10.X.0.0/24 subnets where X is 100-254
	// This gives us 154 possible allocations with 254 containers each
	for i := 100; i <= 254; i++ {
		subnetStr := fmt.Sprintf("10.%d.0.0/24", i)
		_, subnet, err := net.ParseCIDR(subnetStr)
		if err != nil {
			continue
		}

		if !m.allocatedSubnets[subnet.String()] {
			m.allocatedSubnets[subnet.String()] = true
			// Gateway is .1
			gateway := net.ParseIP(fmt.Sprintf("10.%d.0.1", i))
			return subnet, gateway, nil
		}
	}

	return nil, nil, fmt.Errorf("no available subnets")
}

// setupAllocNetwork executes the commands to set up the network
func (m *NetworkManager) setupAllocNetwork(ctx context.Context, info *NetworkInfo, host string) error {
	// Commands to set up the network
	// These need to be run on the host where containerd is running

	commands := []struct {
		desc string
		cmd  []string
	}{
		// Create bridge
		{"create bridge", []string{"brctl", "addbr", info.BridgeName}},
		{"set bridge up", []string{"ip", "link", "set", info.BridgeName, "up"}},
		{"add bridge IP", []string{"ip", "addr", "add", fmt.Sprintf("%s/24", info.Gateway), "dev", info.BridgeName}},

		// Create network namespace
		{"create namespace", []string{"ip", "netns", "add", filepath.Base(info.NamespacePath)}},

		// Create veth pair
		{"create veth pair", []string{"ip", "link", "add", info.VethHost, "type", "veth", "peer", "name", info.VethNS}},
		{"attach veth to bridge", []string{"brctl", "addif", info.BridgeName, info.VethHost}},
		{"set veth host up", []string{"ip", "link", "set", info.VethHost, "up"}},

		// Move veth to namespace
		{"move veth to ns", []string{"ip", "link", "set", info.VethNS, "netns", filepath.Base(info.NamespacePath)}},

		// Configure namespace interface
		{"set lo up in ns", []string{"ip", "netns", "exec", filepath.Base(info.NamespacePath), "ip", "link", "set", "lo", "up"}},
		{"set veth up in ns", []string{"ip", "netns", "exec", filepath.Base(info.NamespacePath), "ip", "link", "set", info.VethNS, "up"}},
	}

	// Execute commands based on whether we're remote or local
	for _, c := range commands {
		var cmd *exec.Cmd
		if host != "" && !strings.HasPrefix(host, "/") {
			// Remote host - run via SSH
			if strings.HasPrefix(host, "ssh://") {
				host = strings.TrimPrefix(host, "ssh://")
			}
			sshArgs := append([]string{host, "sudo"}, c.cmd...)
			cmd = exec.CommandContext(ctx, "ssh", sshArgs...)
		} else {
			// Local execution
			cmd = exec.CommandContext(ctx, "sudo", c.cmd...)
		}

		output, err := cmd.CombinedOutput()
		if err != nil {
			// Some commands might fail if already exists, log but continue
			if strings.Contains(string(output), "already exists") ||
				strings.Contains(string(output), "File exists") {
				log.Printf("Warning: %s (already exists): %s", c.desc, output)
				continue
			}
			return fmt.Errorf("%s failed: %w: %s", c.desc, err, output)
		}
		log.Printf("Network setup: %s completed", c.desc)
	}

	// Set up NAT for internet access
	if err := m.setupNAT(ctx, info, host); err != nil {
		return fmt.Errorf("failed to setup NAT: %w", err)
	}

	// Add security rules to block host and Tailscale access
	if err := m.setupSecurityRules(ctx, info, host); err != nil {
		return fmt.Errorf("failed to setup security rules: %w", err)
	}

	return nil
}

// setupNAT configures NAT for internet access
func (m *NetworkManager) setupNAT(ctx context.Context, info *NetworkInfo, host string) error {
	// Enable IP forwarding
	commands := [][]string{
		{"sysctl", "-w", "net.ipv4.ip_forward=1"},
		// NAT for internet access
		{"iptables", "-t", "nat", "-A", "POSTROUTING", "-s", info.Subnet.String(), "!", "-d", info.Subnet.String(), "-j", "MASQUERADE"},
		// Allow forwarding
		{"iptables", "-A", "FORWARD", "-s", info.Subnet.String(), "-j", "ACCEPT"},
		{"iptables", "-A", "FORWARD", "-d", info.Subnet.String(), "-m", "state", "--state", "RELATED,ESTABLISHED", "-j", "ACCEPT"},
	}

	for _, cmd := range commands {
		execCmd := m.execSSHCommand(ctx, host, cmd...)

		if output, err := execCmd.CombinedOutput(); err != nil {
			log.Printf("Warning: NAT setup command %v failed: %s", cmd, output)
			// Continue - some rules might already exist
		}
	}

	return nil
}

// setupSecurityRules adds iptables rules to block host and Tailscale access
func (m *NetworkManager) setupSecurityRules(ctx context.Context, info *NetworkInfo, host string) error {
	// Get Tailscale interface (usually tailscale0 or utun)
	tailscaleIface := m.getTailscaleInterface(ctx, host)

	commands := [][]string{
		// Block access to host from container subnet (except for established connections)
		{"iptables", "-I", "INPUT", "-s", info.Subnet.String(), "-j", "DROP"},
		// Allow established connections back to containers
		{"iptables", "-I", "INPUT", "-s", info.Subnet.String(), "-m", "state", "--state", "ESTABLISHED,RELATED", "-j", "ACCEPT"},
	}

	// Block Tailscale if interface found
	if tailscaleIface != "" {
		commands = append(commands,
			[]string{"iptables", "-I", "FORWARD", "-s", info.Subnet.String(), "-o", tailscaleIface, "-j", "DROP"},
			[]string{"iptables", "-I", "FORWARD", "-i", tailscaleIface, "-d", info.Subnet.String(), "-j", "DROP"},
		)
	}

	// Block common private network ranges (except the container's own subnet)
	privateRanges := []string{"192.168.0.0/16", "172.16.0.0/12", "10.0.0.0/8"}
	for _, privRange := range privateRanges {
		if privRange != "10.0.0.0/8" { // We use 10.x for containers, be more specific
			commands = append(commands,
				[]string{"iptables", "-I", "FORWARD", "-s", info.Subnet.String(), "-d", privRange, "-j", "DROP"},
			)
		} else {
			// Block 10.0.0.0/8 except our allocated ranges (10.100-254.0.0/24)
			commands = append(commands,
				[]string{"iptables", "-I", "FORWARD", "-s", info.Subnet.String(), "-d", "10.0.0.0/16", "-j", "DROP"},
				[]string{"iptables", "-I", "FORWARD", "-s", info.Subnet.String(), "-d", "10.1.0.0/16", "-j", "DROP"},
				// Continue for other ranges we want to block...
			)
		}
	}

	for _, cmd := range commands {
		execCmd := m.execSSHCommand(ctx, host, cmd...)

		if output, err := execCmd.CombinedOutput(); err != nil {
			log.Printf("Warning: Security rule %v failed: %s", cmd, output)
			// Continue - some rules might already exist
		}
	}

	return nil
}

// getTailscaleInterface finds the Tailscale network interface
func (m *NetworkManager) getTailscaleInterface(ctx context.Context, host string) string {
	// Parse SSH format if present
	if strings.HasPrefix(host, "ssh://") {
		host = strings.TrimPrefix(host, "ssh://")
	}

	// Host is required
	if host == "" || strings.HasPrefix(host, "/") {
		return ""
	}

	cmd := exec.CommandContext(ctx, "ssh", host, "ip", "link", "show")

	output, err := cmd.Output()
	if err != nil {
		return ""
	}

	// Look for Tailscale interface names
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		if strings.Contains(line, "tailscale") || strings.Contains(line, "utun") {
			// Extract interface name
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				iface := strings.TrimSuffix(parts[1], ":")
				return iface
			}
		}
	}

	return ""
}

// AllocateContainerIP allocates an IP address for a container in the allocation's network
func (m *NetworkManager) AllocateContainerIP(allocID string, containerID string) (net.IP, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	info, exists := m.networks[allocID]
	if !exists {
		return nil, fmt.Errorf("network not found for alloc %s", allocID)
	}

	// Find next available IP in subnet (starting from .2, .1 is gateway)
	for i := 2; i <= 254; i++ {
		ip := net.ParseIP(fmt.Sprintf("10.%d.0.%d", info.Subnet.IP[1], i))

		// Check if IP is already allocated
		used := false
		for _, cinfo := range info.Containers {
			if cinfo.IPAddress.Equal(ip) {
				used = true
				break
			}
		}

		if !used {
			// Allocate SSH port (starting from 10000)
			sshPort := m.allocateSSHPort()

			info.Containers[containerID] = &ContainerNetworkInfo{
				ContainerID: containerID,
				IPAddress:   ip,
				SSHPort:     sshPort,
			}
			return ip, nil
		}
	}

	return nil, fmt.Errorf("no available IPs in subnet")
}

// allocateSSHPort finds an available port for SSH forwarding
func (m *NetworkManager) allocateSSHPort() int {
	// Start from 10000 and find an available port
	// In production, we should check if port is actually free
	basePort := 10000
	rand.Seed(time.Now().UnixNano())
	return basePort + rand.Intn(10000)
}

// SetupContainerNetworking configures networking for a container
func (m *NetworkManager) SetupContainerNetworking(ctx context.Context, allocID, containerID string, host string) (*ContainerNetworkInfo, error) {
	m.mu.RLock()
	info, exists := m.networks[allocID]
	if !exists {
		m.mu.RUnlock()
		// Create network if it doesn't exist
		var err error
		info, err = m.CreateAllocNetwork(ctx, allocID, host)
		if err != nil {
			return nil, fmt.Errorf("failed to create network for alloc: %w", err)
		}
		m.mu.RLock()
	}

	containerInfo, exists := info.Containers[containerID]
	m.mu.RUnlock()

	if !exists {
		return nil, fmt.Errorf("container %s not found in network", containerID)
	}

	// Set up port forwarding for SSH
	if err := m.setupPortForwarding(ctx, info, containerInfo, host); err != nil {
		return nil, fmt.Errorf("failed to setup port forwarding: %w", err)
	}

	return containerInfo, nil
}

// setupPortForwarding sets up iptables rules for SSH port forwarding
func (m *NetworkManager) setupPortForwarding(ctx context.Context, netInfo *NetworkInfo, containerInfo *ContainerNetworkInfo, host string) error {
	// Forward SSH port from host to container
	commands := [][]string{
		// DNAT for incoming connections
		{"iptables", "-t", "nat", "-A", "PREROUTING", "-p", "tcp", "--dport", fmt.Sprintf("%d", containerInfo.SSHPort),
			"-j", "DNAT", "--to-destination", fmt.Sprintf("%s:22", containerInfo.IPAddress)},
		// Allow forwarded packets
		{"iptables", "-A", "FORWARD", "-p", "tcp", "-d", containerInfo.IPAddress.String(), "--dport", "22",
			"-m", "state", "--state", "NEW,ESTABLISHED,RELATED", "-j", "ACCEPT"},
	}

	for _, cmd := range commands {
		execCmd := m.execSSHCommand(ctx, host, cmd...)

		if output, err := execCmd.CombinedOutput(); err != nil {
			log.Printf("Warning: Port forwarding setup %v failed: %s", cmd, output)
			// Continue - rule might already exist
		}
	}

	log.Printf("Set up SSH port forwarding for container %s: host port %d -> container %s:22",
		containerInfo.ContainerID, containerInfo.SSHPort, containerInfo.IPAddress)

	return nil
}

// CleanupAllocNetwork removes the network namespace and bridge for an allocation
func (m *NetworkManager) CleanupAllocNetwork(ctx context.Context, allocID string, host string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	info, exists := m.networks[allocID]
	if !exists {
		return nil // Already cleaned up
	}

	// Clean up iptables rules
	m.cleanupNetworkRules(ctx, info, host)

	// Delete network namespace and bridge
	commands := [][]string{
		{"ip", "netns", "del", filepath.Base(info.NamespacePath)},
		{"ip", "link", "set", info.BridgeName, "down"},
		{"brctl", "delbr", info.BridgeName},
	}

	for _, cmd := range commands {
		execCmd := m.execSSHCommand(ctx, host, cmd...)

		if output, err := execCmd.CombinedOutput(); err != nil {
			log.Printf("Warning: Cleanup command %v failed: %s", cmd, output)
		}
	}

	delete(m.allocatedSubnets, info.Subnet.String())
	delete(m.networks, allocID)

	return nil
}

// cleanupNetworkRules removes iptables rules for the allocation
func (m *NetworkManager) cleanupNetworkRules(ctx context.Context, info *NetworkInfo, host string) {
	// Remove NAT and forwarding rules
	commands := [][]string{
		{"iptables", "-t", "nat", "-D", "POSTROUTING", "-s", info.Subnet.String(), "!", "-d", info.Subnet.String(), "-j", "MASQUERADE"},
		{"iptables", "-D", "FORWARD", "-s", info.Subnet.String(), "-j", "ACCEPT"},
		{"iptables", "-D", "FORWARD", "-d", info.Subnet.String(), "-m", "state", "--state", "RELATED,ESTABLISHED", "-j", "ACCEPT"},
		{"iptables", "-D", "INPUT", "-s", info.Subnet.String(), "-j", "DROP"},
		{"iptables", "-D", "INPUT", "-s", info.Subnet.String(), "-m", "state", "--state", "ESTABLISHED,RELATED", "-j", "ACCEPT"},
	}

	// Clean up port forwarding rules for each container
	for _, containerInfo := range info.Containers {
		commands = append(commands,
			[]string{"iptables", "-t", "nat", "-D", "PREROUTING", "-p", "tcp", "--dport", fmt.Sprintf("%d", containerInfo.SSHPort),
				"-j", "DNAT", "--to-destination", fmt.Sprintf("%s:22", containerInfo.IPAddress)},
			[]string{"iptables", "-D", "FORWARD", "-p", "tcp", "-d", containerInfo.IPAddress.String(), "--dport", "22",
				"-m", "state", "--state", "NEW,ESTABLISHED,RELATED", "-j", "ACCEPT"},
		)
	}

	for _, cmd := range commands {
		execCmd := m.execSSHCommand(ctx, host, cmd...)

		// Ignore errors - rules might not exist
		execCmd.CombinedOutput()
	}
}

// WriteCNIConfig writes a CNI configuration for a container
func (m *NetworkManager) WriteCNIConfig(allocID, containerID string) (string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	info, exists := m.networks[allocID]
	if !exists {
		return "", fmt.Errorf("network not found for alloc %s", allocID)
	}

	containerInfo, exists := info.Containers[containerID]
	if !exists {
		return "", fmt.Errorf("container %s not found in network", containerID)
	}

	// Create CNI config for this container
	config := map[string]interface{}{
		"cniVersion": "1.0.0",
		"name":       fmt.Sprintf("exe-%s", allocID),
		"plugins": []map[string]interface{}{
			{
				"type":   "ptp", // Point-to-point for container isolation
				"ipMasq": false, // We handle NAT ourselves
				"ipam": map[string]interface{}{
					"type": "static",
					"addresses": []map[string]interface{}{
						{
							"address": fmt.Sprintf("%s/24", containerInfo.IPAddress),
							"gateway": info.Gateway.String(),
						},
					},
					"routes": []map[string]interface{}{
						{"dst": "0.0.0.0/0"},
					},
				},
			},
		},
	}

	// Write config to file
	configPath := fmt.Sprintf("/tmp/cni-%s-%s.json", allocID, containerID)
	configJSON, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to marshal CNI config: %w", err)
	}

	if err := os.WriteFile(configPath, configJSON, 0644); err != nil {
		return "", fmt.Errorf("failed to write CNI config: %w", err)
	}

	return configPath, nil
}
