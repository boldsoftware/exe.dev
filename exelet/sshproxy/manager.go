package sshproxy

import (
	"fmt"
	"log/slog"
	"net"
	"path/filepath"
	"sync"

	api "exe.dev/pkg/api/exe/compute/v1"
)

// Manager manages SSH proxies for instances.
// An SSH proxy is a process that listens on a port.
// Whenever a connection is made to that port,
// the proxy makes a connections to the ssh port on the VM,
// and splices the two connections together.
type Manager interface {
	// Create and start a new SSH proxy for an instance.
	// If a proxy already exists, it is stopped and replaced.
	CreateProxy(instanceID, targetIP string, port int, instanceDir string) error

	// StopProxy stops running a proxy.
	// It returns the port on which the proxy was running.
	StopProxy(instanceID string) (int, error)

	// GetPort returns the port for an instance.
	// The bool reports whether there is a port.
	GetPort(instanceID string) (int, bool)

	// RecoverProxies finds any existing proxies.
	// This is used when exelet restarts.
	RecoverProxies(instances []*api.Instance) error
}

// scoatManager manages SSH proxies for instances,
// using socat for each proxy.
type socatManager struct {
	mu      sync.Mutex
	proxies map[string]*socatSSHProxy // instanceID -> proxy
	ports   map[string]int            // instanceID -> port
	dataDir string                    // Root directory for instance data
	bindIP  string                    // IP address to bind proxies to (empty means all interfaces)
	log     *slog.Logger
}

// NewManager creates a new SSH proxy manager.
// bindIP specifies the IP address to bind proxies to; empty string means all interfaces.
func NewManager(dataDir, bindIP string, log *slog.Logger) Manager {
	return &socatManager{
		proxies: make(map[string]*socatSSHProxy),
		ports:   make(map[string]int),
		dataDir: dataDir,
		bindIP:  bindIP,
		log:     log,
	}
}

// CreateProxy creates and starts a new SSH proxy for an instance.
// If a proxy already exists for the instance, it is stopped and replaced.
func (m *socatManager) CreateProxy(instanceID, targetIP string, port int, instanceDir string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Stop existing proxy if present (handles restart case and stale proxies)
	if existingProxy, exists := m.proxies[instanceID]; exists {
		m.log.Info("stopping existing proxy before creating new one", "instance", instanceID)
		if err := existingProxy.stop(); err != nil {
			m.log.Warn("failed to stop existing proxy", "instance", instanceID, "error", err)
		}
		delete(m.proxies, instanceID)
		delete(m.ports, instanceID)
	}

	// Create and start proxy
	proxy := newSocatSSHProxy(instanceID, port, targetIP, instanceDir, m.bindIP, m.log)
	if err := proxy.start(); err != nil {
		return fmt.Errorf("failed to start proxy: %w", err)
	}

	// Track proxy
	m.proxies[instanceID] = proxy
	m.ports[instanceID] = port

	return nil
}

// StopProxy stops and removes a proxy for an instance
func (m *socatManager) StopProxy(instanceID string) (int, error) {
	m.mu.Lock()
	proxy, exists := m.proxies[instanceID]
	port := m.ports[instanceID]
	delete(m.proxies, instanceID)
	delete(m.ports, instanceID)
	m.mu.Unlock()

	if !exists {
		return 0, fmt.Errorf("no proxy found for instance %s", instanceID)
	}

	if err := proxy.stop(); err != nil {
		return port, fmt.Errorf("failed to stop proxy: %w", err)
	}

	return port, nil
}

// GetPort returns the port for an instance
func (m *socatManager) GetPort(instanceID string) (int, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	port, exists := m.ports[instanceID]
	return port, exists
}

// StopAll stops all proxies
func (m *socatManager) StopAll() {
	m.mu.Lock()
	proxies := make([]*socatSSHProxy, 0, len(m.proxies))
	for _, proxy := range m.proxies {
		proxies = append(proxies, proxy)
	}
	m.proxies = make(map[string]*socatSSHProxy)
	m.ports = make(map[string]int)
	m.mu.Unlock()

	for _, proxy := range proxies {
		if err := proxy.stop(); err != nil {
			m.log.Error("failed to stop proxy", "instance", proxy.instanceID, "error", err)
		}
	}
}

// RecoverProxies scans instance directories and recovers existing socat processes
// This is called on exelet startup to restore proxy state
func (m *socatManager) RecoverProxies(instances []*api.Instance) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, instance := range instances {
		// Skip stopped instances
		if instance.State == api.VMState_STOPPED {
			continue
		}

		// Get port and target IP from instance config
		port := int(instance.SSHPort)
		if port == 0 {
			m.log.Warn("instance has no SSH port configured", "instance", instance.ID)
			continue
		}

		targetIP := ""
		if instance.VMConfig != nil && instance.VMConfig.NetworkInterface != nil && instance.VMConfig.NetworkInterface.IP != nil {
			if ipStr := instance.VMConfig.NetworkInterface.IP.IPV4; ipStr != "" {
				ipAddr, _, err := net.ParseCIDR(ipStr)
				if err != nil {
					m.log.Warn("failed to parse VM IP", "instance", instance.ID, "ip", ipStr, "error", err)
					continue
				}
				targetIP = ipAddr.String()
			}
		}
		if targetIP == "" {
			m.log.Warn("instance has no target IP configured", "instance", instance.ID)
			continue
		}

		instanceDir := filepath.Join(m.dataDir, "instances", instance.ID)
		proxy := newSocatSSHProxy(instance.ID, port, targetIP, instanceDir, m.bindIP, m.log)

		// Try to load existing metadata (may not exist for older instances)
		if err := proxy.loadFromDisk(); err != nil {
			m.log.Debug("no proxy metadata on disk", "instance", instance.ID)
		}

		// Check if proxy is alive (by PID if we have metadata, or by port)
		if proxy.isRunning() {
			// Proxy is running, adopt it
			m.log.Info("recovered running proxy", "instance", instance.ID, "port", proxy.port, "pid", proxy.pid)
			m.proxies[instance.ID] = proxy
			m.ports[instance.ID] = proxy.port
		} else {
			// Proxy is dead or no metadata, try to start/adopt
			m.log.Info("starting proxy for running instance", "instance", instance.ID, "port", proxy.port)
			if err := proxy.start(); err != nil {
				m.log.Error("failed to start proxy", "instance", instance.ID, "error", err)
				continue
			}
			m.proxies[instance.ID] = proxy
			m.ports[instance.ID] = proxy.port
		}
	}

	return nil
}

// MarkPortAllocated marks a port as allocated (for port allocator integration)
func (m *socatManager) MarkPortAllocated(instanceID string, port int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ports[instanceID] = port
}

// GetAllocatedPorts returns all currently allocated ports
func (m *socatManager) GetAllocatedPorts() []int {
	m.mu.Lock()
	defer m.mu.Unlock()

	ports := make([]int, 0, len(m.ports))
	for _, port := range m.ports {
		ports = append(ports, port)
	}
	return ports
}
