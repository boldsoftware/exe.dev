package tcpproxy

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
)

// TCPProxy represents a TCP proxy that forwards connections
type TCPProxy struct {
	listenAddr string
	targetAddr string
	listener   net.Listener
	log        *slog.Logger
	ctx        context.Context
	cancel     context.CancelFunc
	wg         sync.WaitGroup
}

// NewTCPProxy creates a new TCP proxy
func NewTCPProxy(listenPort int, targetIP string, targetPort int, log *slog.Logger) *TCPProxy {
	ctx, cancel := context.WithCancel(context.Background())
	return &TCPProxy{
		listenAddr: fmt.Sprintf("0.0.0.0:%d", listenPort),
		targetAddr: fmt.Sprintf("%s:%d", targetIP, targetPort),
		log:        log,
		ctx:        ctx,
		cancel:     cancel,
	}
}

// Start starts the TCP proxy
func (p *TCPProxy) Start() error {
	listener, err := net.Listen("tcp", p.listenAddr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", p.listenAddr, err)
	}
	p.listener = listener

	p.log.Debug("tcp proxy started", "listen", p.listenAddr, "target", p.targetAddr)

	p.wg.Add(1)
	go p.acceptLoop()

	return nil
}

// acceptLoop accepts incoming connections and forwards them
func (p *TCPProxy) acceptLoop() {
	defer p.wg.Done()

	for {
		select {
		case <-p.ctx.Done():
			return
		default:
		}

		conn, err := p.listener.Accept()
		if err != nil {
			select {
			case <-p.ctx.Done():
				return
			default:
				p.log.Error("failed to accept connection", "error", err)
				continue
			}
		}

		p.wg.Add(1)
		go p.handleConnection(conn)
	}
}

// handleConnection handles a single connection by forwarding it to the target
func (p *TCPProxy) handleConnection(clientConn net.Conn) {
	defer p.wg.Done()
	defer clientConn.Close()

	// Connect to target
	targetConn, err := net.Dial("tcp", p.targetAddr)
	if err != nil {
		p.log.Error("failed to connect to target", "target", p.targetAddr, "error", err)
		return
	}
	defer targetConn.Close()

	// Bidirectional copy
	done := make(chan struct{}, 2)

	// Client -> Target
	go func() {
		io.Copy(targetConn, clientConn)
		done <- struct{}{}
	}()

	// Target -> Client
	go func() {
		io.Copy(clientConn, targetConn)
		done <- struct{}{}
	}()

	// Wait for one direction to finish
	select {
	case <-done:
	case <-p.ctx.Done():
	}
}

// Stop stops the TCP proxy
func (p *TCPProxy) Stop() error {
	p.cancel()
	if p.listener != nil {
		p.listener.Close()
	}
	p.wg.Wait()
	p.log.Debug("tcp proxy stopped", "listen", p.listenAddr)
	return nil
}

// ProxyManager manages TCP proxies for instances
type ProxyManager struct {
	mu      sync.Mutex
	proxies map[string]*TCPProxy // instanceID -> proxy
	ports   map[string]int       // instanceID -> port
	log     *slog.Logger
}

// NewProxyManager creates a new proxy manager
func NewProxyManager(log *slog.Logger) *ProxyManager {
	return &ProxyManager{
		proxies: make(map[string]*TCPProxy),
		ports:   make(map[string]int),
		log:     log,
	}
}

// AddProxy adds a proxy for an instance
func (pm *ProxyManager) AddProxy(instanceID string, proxy *TCPProxy, port int) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.proxies[instanceID] = proxy
	pm.ports[instanceID] = port
}

// RemoveProxy removes and stops a proxy for an instance
func (pm *ProxyManager) RemoveProxy(instanceID string) (int, error) {
	pm.mu.Lock()
	proxy, exists := pm.proxies[instanceID]
	port := pm.ports[instanceID]
	delete(pm.proxies, instanceID)
	delete(pm.ports, instanceID)
	pm.mu.Unlock()

	if !exists {
		return 0, fmt.Errorf("no proxy found for instance %s", instanceID)
	}

	return port, proxy.Stop()
}

// GetPort returns the port for an instance
func (pm *ProxyManager) GetPort(instanceID string) (int, bool) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	port, exists := pm.ports[instanceID]
	return port, exists
}

// StopAll stops all proxies
func (pm *ProxyManager) StopAll() {
	pm.mu.Lock()
	proxies := make([]*TCPProxy, 0, len(pm.proxies))
	for _, proxy := range pm.proxies {
		proxies = append(proxies, proxy)
	}
	pm.proxies = make(map[string]*TCPProxy)
	pm.ports = make(map[string]int)
	pm.mu.Unlock()

	for _, proxy := range proxies {
		proxy.Stop()
	}
}
