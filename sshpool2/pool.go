package sshpool2

import (
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"
	"tailscale.com/util/singleflight"
)

// connKey uniquely identifies an SSH connection
type connKey struct {
	host      string
	user      string
	port      int
	publicKey string // SSH public key
}

func (k connKey) String() string {
	return fmt.Sprintf("%s@%s:%d", k.user, k.host, k.port)
}

// pooledConn wraps an SSH client with expiration tracking
type pooledConn struct {
	client  *ssh.Client // immutable after creation
	key     connKey     // immutable after creation
	pool    *Pool       // immutable after creation
	removed atomic.Bool // can be set to true, never back to false

	mu          sync.Mutex
	expireTimer *time.Timer // protected by mu
	active      int         // protected by mu - number of active dials using this connection
}

// trackedConn wraps net.Conn to track when it's released
type trackedConn struct {
	net.Conn
	pc *pooledConn
}

func (tc *trackedConn) Close() error {
	err := tc.Conn.Close()
	tc.pc.releaseConn()
	return err
}

func (pc *pooledConn) acquireConn() {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	pc.active++
	// Reset expiry timer on each use
	if pc.expireTimer != nil {
		pc.expireTimer.Stop()
	}
	pc.expireTimer = time.AfterFunc(pc.pool.ttl, func() {
		pc.pool.removeConn(pc.key)
	})
}

func (pc *pooledConn) releaseConn() {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	pc.active--
	if pc.active < 0 {
		panic(fmt.Sprintf("releaseConn: active count went negative: %d", pc.active))
	}

	// If this was the last active connection and the pooled connection was removed,
	// close the underlying SSH client
	if pc.active == 0 && pc.removed.Load() {
		go func() {
			slog.Debug("closing SSH connection after last active connection released", "key", pc.key.String())
			if err := pc.close(); err != nil {
				slog.Warn("error closing SSH connection", "key", pc.key.String(), "error", err)
			}
		}()
	}
}

func (pc *pooledConn) close() error {
	pc.mu.Lock()
	defer pc.mu.Unlock()

	if pc.expireTimer != nil {
		pc.expireTimer.Stop()
		pc.expireTimer = nil
	}

	if pc.client != nil {
		return pc.client.Close()
	}
	return nil
}

func (pc *pooledConn) canClose() bool {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	return pc.active == 0
}

// Pool manages pooled SSH connections with expiration
type Pool struct {
	ttl time.Duration

	mu    sync.Mutex
	conns map[connKey]*pooledConn

	sfGroup singleflight.Group[connKey, *pooledConn]
}

// New creates a new SSH connection pool with the specified TTL.
// Connections expire after TTL of inactivity.
func New(ttl time.Duration) *Pool {
	return &Pool{
		ttl:   ttl,
		conns: make(map[connKey]*pooledConn),
	}
}

// Dial dials the target address through a pooled SSH connection.
// If a valid cached connection exists, it reuses that. Otherwise, it
// creates a new connection. Multiple concurrent calls with the same
// parameters will result in only one connection attempt (via singleflight).
// The signer is used to create a unique key for the connection.
func (p *Pool) Dial(network, addr, host, user string, port int, signer ssh.Signer, config *ssh.ClientConfig) (net.Conn, error) {
	// Create a key from the signer's public key
	pubKey := signer.PublicKey()
	keyStr := string(pubKey.Marshal())

	key := connKey{
		host:      host,
		user:      user,
		port:      port,
		publicKey: keyStr,
	}

	// Check if we have a valid cached connection
	p.mu.Lock()
	pc, ok := p.conns[key]
	if ok && !pc.removed.Load() {
		// Acquire before releasing the pool lock to prevent removal race
		pc.acquireConn()
		p.mu.Unlock()
		slog.Debug("reusing pooled SSH connection", "key", key.String())
		return p.dialThroughClient(pc, network, addr)
	}
	p.mu.Unlock()

	// No cached connection, create one using singleflight
	var err error
	pc, err, _ = p.sfGroup.Do(key, func() (*pooledConn, error) {
		return p.connect(host, port, config, key)
	})
	if err != nil {
		return nil, err
	}

	pc.acquireConn()
	return p.dialThroughClient(pc, network, addr)
}

// dialThroughClient dials through the SSH client and wraps the connection
func (p *Pool) dialThroughClient(pc *pooledConn, network, addr string) (net.Conn, error) {
	// Dial the target address through the SSH connection with retries.
	// Retries are needed because there can be a race between when a service
	// starts listening and when we try to connect to it.
	var conn net.Conn
	var err error
	remoteRetries := []time.Duration{
		0, 100 * time.Millisecond, 200 * time.Millisecond,
		500 * time.Millisecond, 1 * time.Second, 2 * time.Second, 0,
	}

	for i, wait := range remoteRetries {
		conn, err = pc.client.Dial(network, addr)
		if err == nil {
			break
		}
		if i < len(remoteRetries)-1 && wait > 0 {
			time.Sleep(wait)
		}
	}

	if err != nil {
		pc.releaseConn()
		// Don't remove the connection from the pool - the SSH connection is fine,
		// it's just that the target port isn't listening. The connection can be
		// reused for other dials.
		return nil, fmt.Errorf("failed to dial %s via SSH: %w", addr, err)
	}

	return &trackedConn{Conn: conn, pc: pc}, nil
}

// connect establishes a new SSH connection with retry logic
func (p *Pool) connect(host string, port int, config *ssh.ClientConfig, key connKey) (*pooledConn, error) {
	start := time.Now()
	addr := net.JoinHostPort(host, strconv.Itoa(port))

	// Establish TCP connection with a reasonable timeout
	dialer := &net.Dialer{Timeout: 30 * time.Second}
	tcpConn, err := dialer.Dial("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("failed to dial %s: %w", addr, err)
	}

	// Perform SSH handshake with retries
	var client *ssh.Client
	sshRetries := []time.Duration{
		100 * time.Millisecond,
		200 * time.Millisecond,
		500 * time.Millisecond,
		1 * time.Second,
		1 * time.Second,
		2 * time.Second,
		3 * time.Second,
		0, // final attempt
	}

	attempts := 0
	for i, wait := range sshRetries {
		attempts++
		cconn, chans, reqs, err := ssh.NewClientConn(tcpConn, addr, config)
		if err == nil {
			client = ssh.NewClient(cconn, chans, reqs)
			break
		}

		if i == len(sshRetries)-1 {
			tcpConn.Close()
			return nil, fmt.Errorf("SSH handshake failed after %d attempts: %w", len(sshRetries), err)
		}

		time.Sleep(wait)
	}

	latency := time.Since(start)
	slog.Info("established new SSH connection in pool", "key", key.String(), "attempts", attempts, "latency_ms", latency.Milliseconds())

	// Store in pool
	pc := &pooledConn{
		client: client,
		key:    key,
		pool:   p,
	}

	// Set initial expiry timer
	pc.expireTimer = time.AfterFunc(p.ttl, func() {
		p.removeConn(key)
	})

	p.mu.Lock()
	p.conns[key] = pc
	p.mu.Unlock()

	return pc, nil
}

// removeConn removes a connection from the pool.
// The actual SSH client will be closed when the last active connection is released.
func (p *Pool) removeConn(key connKey) {
	p.mu.Lock()
	pc, ok := p.conns[key]
	if !ok {
		p.mu.Unlock()
		return
	}
	pc.removed.Store(true)
	delete(p.conns, key)
	p.mu.Unlock()

	// If there are no active connections, close immediately
	if pc.canClose() {
		slog.Debug("closing SSH connection immediately (no active connections)", "key", key.String())
		if err := pc.close(); err != nil {
			slog.Warn("error closing SSH connection", "key", key.String(), "error", err)
		}
	}
	// Otherwise, the last active connection will close it in releaseConn()
}

// Close shuts down the pool and closes all connections immediately.
func (p *Pool) Close() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	conns := make([]*pooledConn, 0, len(p.conns))
	for _, pc := range p.conns {
		pc.removed.Store(true)
		conns = append(conns, pc)
	}
	p.conns = nil
	p.mu.Unlock()

	// Close all connections immediately
	for _, pc := range conns {
		if err := pc.close(); err != nil {
			slog.Warn("error closing SSH connection during pool shutdown", "key", pc.key.String(), "error", err)
		}
	}

	return nil
}
