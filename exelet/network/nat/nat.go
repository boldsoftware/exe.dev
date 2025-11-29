package nat

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"

	"exe.dev/pkg/dhcpd"
)

const (
	DefaultBridgeName = "br-exe"
	DefaultNetwork    = "10.42.0.0/16"
	DefaultNameserver = "1.1.1.1"
	DefaultNTPServer  = "ntp.ubuntu.com"
	MetadataIP        = "169.254.169.254"

	DeviceName = "eth0"

	// IPCleanupInterval is how often to check for orphaned IPs
	IPCleanupInterval = 1 * time.Minute

	// IPReleaseGracePeriod is how long an IP must be orphaned before release
	// This prevents rapid IP reuse which can cause ARP cache and connection state issues
	IPReleaseGracePeriod = 10 * time.Minute
)

// ErrNotImplemented is returned for functionality that is not implemented
var ErrNotImplemented = errors.New("not implemented")

// Config is the NAT specific configuration
type Config struct {
	Bridge  string
	Network string
	Router  string
}

type NAT struct {
	bridgeName     string
	network        string
	dhcpServer     *dhcpd.DHCPServer
	nameservers    []string
	ntpServer      string
	router         string
	mu             *sync.Mutex
	availableIPs   map[string]net.IP
	allocatedIPs   []net.IP
	orphanedIPs    map[string]time.Time // IP -> first seen orphaned time (for grace period)
	cleanupCancel  context.CancelFunc
	dhcpCancel     context.CancelFunc   // cancel function for DHCP server context
	log            *slog.Logger
}

func NewNATManager(addr string, log *slog.Logger) (*NAT, error) {
	u, err := url.Parse(addr)
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(u.Scheme, "nat") {
		return nil, fmt.Errorf("invalid configuration specified for NAT manager: %s", addr)
	}

	if u.Path == "" {
		return nil, fmt.Errorf("path must be specified for nat network manager (e.g. nat:///tmp)")
	}

	bridgeName := DefaultBridgeName
	network := DefaultNetwork
	nameservers := []string{DefaultNameserver}
	ntpServer := DefaultNTPServer
	router := ""
	// configure bridge
	if v := u.Query().Get("bridge"); v != "" {
		bridgeName = v
	}
	if v := u.Query().Get("network"); v != "" {
		network = v
	}
	if v := u.Query().Get("dns"); v != "" {
		nameservers = strings.Split(v, ",")
	}
	if v := u.Query().Get("ntp"); v != "" {
		ntpServer = v
	}
	if v := u.Query().Get("router"); v != "" {
		router = v
	}

	// configure DHCP server
	dhcpSrv, err := dhcpd.NewDHCPServer(&dhcpd.Config{
		Interface:  bridgeName,
		DataDir:    u.Path,
		Network:    network,
		Port:       67,
		DNSServers: nameservers,
	}, log)
	if err != nil {
		return nil, err
	}

	// If router not explicitly set, use the bridge IP (first address in network)
	if router == "" {
		serverIP, err := dhcpSrv.ServerIP()
		if err != nil {
			return nil, fmt.Errorf("failed to get server IP: %w", err)
		}
		router = serverIP.String()
	}

	// Create cancellable context for cleanup goroutine
	cleanupCtx, cleanupCancel := context.WithCancel(context.Background())

	n := &NAT{
		bridgeName:    bridgeName,
		network:       network,
		dhcpServer:    dhcpSrv,
		nameservers:   nameservers,
		ntpServer:     ntpServer,
		router:        router,
		mu:            &sync.Mutex{},
		orphanedIPs:   make(map[string]time.Time),
		cleanupCancel: cleanupCancel,
		log:           log,
	}

	// Start periodic orphaned IP cleanup goroutine
	go n.cleanupOrphanedIPs(cleanupCtx)

	return n, nil
}

// Close stops the NAT manager and cleans up resources
func (n *NAT) Close() error {
	// Cancel cleanup goroutine
	if n.cleanupCancel != nil {
		n.cleanupCancel()
	}

	// Cancel DHCP server context
	if n.dhcpCancel != nil {
		n.dhcpCancel()
	}

	// Stop DHCP server to close socket
	if n.dhcpServer != nil {
		if err := n.dhcpServer.Stop(); err != nil {
			n.log.Warn("failed to stop DHCP server", "error", err)
		}
	}

	return nil
}

// cleanupOrphanedIPs periodically checks for orphaned IPs and releases them after grace period
func (n *NAT) cleanupOrphanedIPs(ctx context.Context) {
	ticker := time.NewTicker(IPCleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n.processOrphanedIPs()
		}
	}
}

// processOrphanedIPs implements stateless cleanup by comparing DHCP leases with active interfaces
func (n *NAT) processOrphanedIPs() {
	// Get all DHCP leases
	leases, err := n.dhcpServer.ListLeases()
	if err != nil {
		n.log.Warn("failed to list DHCP leases for cleanup", "error", err)
		return
	}

	// Get all active tap interfaces with their MAC addresses
	activeTaps, err := n.listTapInterfaces()
	if err != nil {
		n.log.Warn("failed to list tap interfaces for cleanup", "error", err)
		return
	}

	// Build a set of active MAC addresses from tap interfaces
	activeMACSet := make(map[string]bool)
	for _, tap := range activeTaps {
		activeMACSet[tap.MACAddress] = true
	}

	n.mu.Lock()
	defer n.mu.Unlock()

	now := time.Now()
	currentOrphans := make(map[string]bool)

	// Check each DHCP lease
	for _, lease := range leases {
		// If the lease's MAC address is not in the active tap set, it's orphaned
		if !activeMACSet[lease.MACAddress] {
			currentOrphans[lease.IP] = true
		}
	}

	// Collect IPs to release (those that exceeded grace period)
	toRelease := []string{}
	for ip := range n.orphanedIPs {
		if !currentOrphans[ip] {
			// IP is no longer orphaned (interface came back)
			delete(n.orphanedIPs, ip)
			continue
		}

		// Check if grace period has elapsed
		firstSeen := n.orphanedIPs[ip]
		if now.Sub(firstSeen) >= IPReleaseGracePeriod {
			toRelease = append(toRelease, ip)
		}
	}

	// Release all orphaned IPs in a single batch operation
	if len(toRelease) > 0 {
		if err := n.dhcpServer.ReleaseBatch(toRelease); err != nil {
			n.log.Warn("failed to release orphaned IPs", "count", len(toRelease), "error", err)
		} else {
			n.log.Debug("released orphaned IPs after grace period", "count", len(toRelease))
			// Remove released IPs from tracking
			for _, ip := range toRelease {
				delete(n.orphanedIPs, ip)
			}
		}
	}

	// Add newly detected orphans
	for ip := range currentOrphans {
		if _, exists := n.orphanedIPs[ip]; !exists {
			n.orphanedIPs[ip] = now
		}
	}
}

