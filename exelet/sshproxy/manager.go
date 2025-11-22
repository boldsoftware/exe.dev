package sshproxy

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"

	api "exe.dev/pkg/api/exe/compute/v1"
)

// Manager manages SSH proxies for instances
type Manager struct {
	mu      sync.Mutex
	proxies map[string]*SSHProxy // instanceID -> proxy
	ports   map[string]int       // instanceID -> port
	dataDir string               // Root directory for instance data
	log     *slog.Logger
}

// NewManager creates a new SSH proxy manager
func NewManager(dataDir string, log *slog.Logger) *Manager {
	return &Manager{
		proxies: make(map[string]*SSHProxy),
		ports:   make(map[string]int),
		dataDir: dataDir,
		log:     log,
	}
}

// CreateProxy creates and starts a new SSH proxy for an instance
func (m *Manager) CreateProxy(instanceID, targetIP string, port int, instanceDir string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check if proxy already exists
	if _, exists := m.proxies[instanceID]; exists {
		return fmt.Errorf("proxy already exists for instance %s", instanceID)
	}

	// Create and start proxy
	proxy := NewSSHProxy(instanceID, port, targetIP, instanceDir, m.log)
	if err := proxy.Start(); err != nil {
		return fmt.Errorf("failed to start proxy: %w", err)
	}

	// Track proxy
	m.proxies[instanceID] = proxy
	m.ports[instanceID] = port

	return nil
}

// StopProxy stops and removes a proxy for an instance
func (m *Manager) StopProxy(instanceID string) (int, error) {
	m.mu.Lock()
	proxy, exists := m.proxies[instanceID]
	port := m.ports[instanceID]
	delete(m.proxies, instanceID)
	delete(m.ports, instanceID)
	m.mu.Unlock()

	if !exists {
		return 0, fmt.Errorf("no proxy found for instance %s", instanceID)
	}

	if err := proxy.Stop(); err != nil {
		return port, fmt.Errorf("failed to stop proxy: %w", err)
	}

	return port, nil
}

// GetPort returns the port for an instance
func (m *Manager) GetPort(instanceID string) (int, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	port, exists := m.ports[instanceID]
	return port, exists
}

// StopAll stops all proxies (called on exelet shutdown)
func (m *Manager) StopAll() {
	m.mu.Lock()
	proxies := make([]*SSHProxy, 0, len(m.proxies))
	for _, proxy := range m.proxies {
		proxies = append(proxies, proxy)
	}
	m.proxies = make(map[string]*SSHProxy)
	m.ports = make(map[string]int)
	m.mu.Unlock()

	for _, proxy := range proxies {
		if err := proxy.Stop(); err != nil {
			m.log.Error("failed to stop proxy", "instance", proxy.InstanceID, "error", err)
		}
	}
}

// RecoverProxies scans instance directories and recovers existing socat processes
// This is called on exelet startup to restore proxy state
func (m *Manager) RecoverProxies(instances []*api.Instance) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, instance := range instances {
		instanceDir := filepath.Join(m.dataDir, "instances", instance.ID)

		// Check if proxy metadata exists
		metadataPath := filepath.Join(instanceDir, "process-sshproxy.json")
		if _, err := os.Stat(metadataPath); os.IsNotExist(err) {
			// No proxy metadata, skip
			continue
		}

		// Create proxy object and load metadata
		proxy := NewSSHProxy(instance.ID, 0, "", instanceDir, m.log)
		if err := proxy.LoadFromDisk(); err != nil {
			m.log.Warn("failed to load proxy metadata", "instance", instance.ID, "error", err)
			continue
		}

		// Get target IP from instance config
		if instance.VMConfig != nil && instance.VMConfig.NetworkInterface != nil && instance.VMConfig.NetworkInterface.IP != nil {
			// The IP is stored in CIDR format (e.g., "192.168.127.2/24"), extract just the IP
			if ipStr := instance.VMConfig.NetworkInterface.IP.IPV4; ipStr != "" {
				ipAddr, _, err := net.ParseCIDR(ipStr)
				if err != nil {
					m.log.Warn("failed to parse VM IP", "instance", instance.ID, "ip", ipStr, "error", err)
					continue
				}
				proxy.TargetIP = ipAddr.String()
			}
		}

		// Check instance state
		if instance.State == api.VMState_STOPPED {
			// Instance is stopped, kill any running proxy
			if proxy.IsRunning() {
				m.log.Info("stopping proxy for stopped instance", "instance", instance.ID, "pid", proxy.PID)
				if err := proxy.Stop(); err != nil {
					m.log.Error("failed to stop proxy", "instance", instance.ID, "error", err)
				}
			}
			continue
		}

		// Instance is running or starting, check if proxy is alive
		if proxy.IsRunning() {
			// Proxy is running, adopt it
			m.log.Info("recovered running proxy", "instance", instance.ID, "port", proxy.Port, "pid", proxy.PID)
			m.proxies[instance.ID] = proxy
			m.ports[instance.ID] = proxy.Port
		} else {
			// Proxy is dead, restart it
			m.log.Info("restarting dead proxy", "instance", instance.ID, "port", proxy.Port)
			if err := proxy.Start(); err != nil {
				m.log.Error("failed to restart proxy", "instance", instance.ID, "error", err)
				continue
			}
			m.proxies[instance.ID] = proxy
			m.ports[instance.ID] = proxy.Port
		}
	}

	return nil
}

// MarkPortAllocated marks a port as allocated (for port allocator integration)
func (m *Manager) MarkPortAllocated(instanceID string, port int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ports[instanceID] = port
}

// GetAllocatedPorts returns all currently allocated ports
func (m *Manager) GetAllocatedPorts() []int {
	m.mu.Lock()
	defer m.mu.Unlock()

	ports := make([]int, 0, len(m.ports))
	for _, port := range m.ports {
		ports = append(ports, port)
	}
	return ports
}
