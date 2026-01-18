// Package sshpool2 provides a pool of SSH connections for dialing through SSH.
package sshpool2

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/crypto/ssh"
	"tailscale.com/util/singleflight"
)

// Metrics holds Prometheus metrics for the SSH connection pool.
type Metrics struct {
	cacheTotal *prometheus.CounterVec
}

// NewMetrics creates and registers pool metrics.
func NewMetrics(registry *prometheus.Registry) *Metrics {
	m := &Metrics{
		cacheTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "sshpool_cache_total",
				Help: "Total number of SSH pool cache lookups.",
			},
			[]string{"result"}, // "hit" or "miss"
		),
	}
	registry.MustRegister(m.cacheTotal)
	return m
}

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

// pooledConn wraps an SSH client with expiration tracking.
type pooledConn struct {
	client *ssh.Client // immutable after creation
	key    connKey     // immutable after creation
	pool   *Pool       // immutable after creation

	mu sync.Mutex // protects following fields
	// active refcounts the number of active connections.
	// It includes connections that have been closed but whose post-close TTL has not yet expired.
	active int
	timer  *time.Timer // timer to close the connection after last active released

	log *slog.Logger
}

// trackedConn informs pc when the connection is closed.
type trackedConn struct {
	net.Conn
	pc   *pooledConn
	once sync.Once
}

func (tc *trackedConn) Close() error {
	err := tc.Conn.Close()
	tc.once.Do(func() {
		tc.pc.disconnected()
	})
	return err
}

// connected informs pc that it is being used for a new connection.
func (pc *pooledConn) connected() {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	pc.active++ // acquire
}

// connect requests that pc be used for a new connection.
// It reports whether the connection was successfully acquired.
func (pc *pooledConn) connect() bool {
	if pc == nil {
		return false
	}
	pc.mu.Lock()
	defer pc.mu.Unlock()
	alive := pc.active > 0
	if alive {
		pc.active++
	}
	return alive
}

// disconnected informs pc that a connection using pc has been closed.
func (pc *pooledConn) disconnected() {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	if pc.timer == nil {
		pc.timer = time.AfterFunc(pc.pool.ttl(), pc.release)
		return
	}
	stopped := pc.timer.Reset(pc.pool.ttl())
	if stopped {
		// We replaced the existing timer before it fired.
		// It is our responsibility to decrement active.
		pc.active--
	}
}

func (pc *pooledConn) release() {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	pc.active--
	switch {
	case pc.active > 0:
		// other connections got added; do nothing
		return
	case pc.active < 0:
		// impossible
		panic(fmt.Sprintf("pooledConn.release: negative active=%d", pc.active))
	}

	if err := pc.client.Close(); err != nil {
		pc.log.Warn("error closing SSH connection", "key", pc.key.String(), "error", err)
	}
	pc.pool.removeConn(pc)
}

// Pool manages pooled SSH connections.
type Pool struct {
	// TTL is the duration after which idle connections expire.
	// A zero TTL is interpreted as 1 minute.
	// Must not be changed after the first use of the Pool.
	// Very short TTLs are a bad idea: they may cause Dial failures in which
	// a connection is closed just after being created but before being used.
	TTL time.Duration

	Logger  *slog.Logger
	Metrics *Metrics

	sfGroup singleflight.Group[connKey, *pooledConn]

	mu    sync.Mutex // guards following fields
	conns map[connKey]*pooledConn
}

func (p *Pool) log() *slog.Logger {
	if p.Logger != nil {
		return p.Logger
	}
	return slog.Default()
}

func (p *Pool) ttl() time.Duration {
	return cmp.Or(p.TTL, time.Minute)
}

func (p *Pool) incCacheResult(result string) {
	if p.Metrics != nil {
		p.Metrics.cacheTotal.WithLabelValues(result).Inc()
	}
}

// DialContext dials the target address through a pooled SSH connection.
//
// network and addr specify the target to dial (e.g., "tcp", "example.com:80").
// host, user, port, and signer specify the SSH connection to use.
//
// Pooling occurs on a per-(host,user,port,publicKey) basis.
// Config is used only when establishing a new SSH connection.
//
// DialContext is a low level function that does no retries.
// The caller is strongly encouraged to use DialWithRetries,
// as there are many ways that dialing through an SSH pool can fail transiently.
func (p *Pool) DialContext(ctx context.Context, network, addr, host, user string, port int, signer ssh.Signer, config *ssh.ClientConfig) (net.Conn, error) {
	key := connKey{
		host:      host,
		user:      user,
		port:      port,
		publicKey: string(signer.PublicKey().Marshal()),
	}
	// Do not pass the context into the singleflight function:
	// Even if the context is canceled, other callers may still want to use the connection.
	pc, err, _ := p.sfGroup.Do(key, func() (*pooledConn, error) {
		return p.connect(key, config)
	})
	if err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return p.dialThroughClient(ctx, pc, network, addr)
}

// DialWithRetries calls Dial with a set of retry delays.
// The returned set of errors contains all errors encountered during dialing, one per failed attempt.
// It may be non-empty even on success.
//
// There are multiple levels at which Dial attempts can fail:
//   - connecting to the SSH host
//   - establishing the SSH session
//   - connecting to the target address via port forwarding
//   - broken pooled connections
//
// This retry loop covers all of these failure modes.
func (p *Pool) DialWithRetries(ctx context.Context, network, addr, host, user string, port int, signer ssh.Signer, config *ssh.ClientConfig, retries []time.Duration) (net.Conn, error) {
	retries = slices.Clone(retries)
	retries = append(retries, 0) // final attempt has no sleep after it
	var errs []error
	for _, delay := range retries {
		if err := ctx.Err(); err != nil {
			errs = append(errs, err)
			return nil, errors.Join(errs...)
		}

		conn, err := p.DialContext(ctx, network, addr, host, user, port, signer, config)
		if err == nil {
			return conn, errors.Join(errs...)
		}
		errs = append(errs, err)

		select {
		case <-time.After(delay):
		case <-ctx.Done():
		}
	}

	if err := ctx.Err(); err != nil {
		errs = append(errs, ctx.Err())
	}
	return nil, errors.Join(errs...)
}

// connect establishes an SSH connection.
// The returned pooledConn is valid for at least p.ttl() duration.
func (p *Pool) connect(key connKey, config *ssh.ClientConfig) (*pooledConn, error) {
	// If we have an existing usable connection in the pool, return it immediately.
	pc := p.getConn(key)
	connected := pc.connect()
	if connected {
		p.incCacheResult("hit")
		pc.disconnected() // balance the connect() call
		return pc, nil
	}
	p.incCacheResult("miss")

	addr := net.JoinHostPort(key.host, strconv.Itoa(key.port))
	prevTimeout := config.Timeout
	config.Timeout = 3 * time.Second // fail fast on new connections
	client, err := ssh.Dial("tcp", addr, config)
	config.Timeout = prevTimeout
	if err != nil {
		return nil, fmt.Errorf("SSH dial failed: %w", err)
	}
	p.log().Info("established new SSH connection in pool", "key", key.String())

	pc = &pooledConn{client: client, key: key, pool: p, log: p.log()}
	// Immediately mark as connected and then add a disconnect for balance.
	// This starts the TTL clock running.
	// Under normal operation, the connection will be used immediately after this.
	pc.connected()
	pc.disconnected()
	p.setConn(pc)

	return pc, nil
}

// dialThroughClient dials through the SSH client and wraps the connection
func (p *Pool) dialThroughClient(ctx context.Context, pc *pooledConn, network, addr string) (net.Conn, error) {
	alive := pc.connect()
	if !alive {
		// Should only happen if there was a very short TTL.
		// This caller should be retrying anyway.
		return nil, fmt.Errorf("dialThroughClient: SSH connection pool entry is unexpectedly dead, is the TTL set low?")
	}
	// Use a short timeout for the port forward dial to quickly detect:
	// - Stale SSH connections (channel open hangs)
	// - Unresponsive backends
	// Note: "port not bound" still fails fast since connection refused is immediate
	shortCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	conn, err := pc.client.DialContext(shortCtx, network, addr)
	if err != nil {
		p.log().InfoContext(ctx, "dial failed", "err", err, "errtype", reflect.TypeOf(err))
		// Make a best-effort attempt to determine whether the dial failed because the underlying SSH connection is dead.
		// If so, remove it from the pool, so that subsequent calls will create a new connection.
		if isSSHConnError(err) {
			p.log().InfoContext(ctx, "dropping dead ssh connection", "key", pc.key.String(), "err", err)
			p.removeConn(pc)
		}
		pc.disconnected() // balance the connect() call
		return nil, fmt.Errorf("failed to dial %s via SSH: %w", addr, err)
		// Set up a tracked connection that calls pc.disconnected() when conn closes.
	}
	return &trackedConn{Conn: conn, pc: pc}, nil
}

func isSSHConnError(err error) bool {
	var openErr *ssh.OpenChannelError
	if errors.As(err, &openErr) {
		return true
	}
	if errors.Is(err, net.ErrClosed) {
		return true
	}
	if errors.Is(err, io.EOF) {
		return true
	}
	// When the underlying SSH transport has closed, the next channel open often fails
	// with a formatted error that doesn't wrap a well-known sentinel.
	if strings.Contains(err.Error(), "ssh: unexpected packet in response to channel open") {
		return true
	}
	// Timeout errors indicate the SSH connection is unresponsive, typically because
	// the remote host rebooted and the TCP connection is now stale (half-open).
	// We should remove these connections from the pool so subsequent requests can
	// establish fresh connections.
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	return false
}

func (p *Pool) getConn(key connKey) *pooledConn {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.conns[key]
}

// setConn adds or updates a connection in the pool.
func (p *Pool) setConn(pc *pooledConn) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.conns == nil {
		p.conns = make(map[connKey]*pooledConn)
	}
	p.conns[pc.key] = pc
}

// removeConn removes a connection from the pool.
// The actual SSH client will be closed when the last active connection is released.
func (p *Pool) removeConn(pc *pooledConn) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.conns == nil {
		return
	}
	// It is possible (albeit unlikely) that pc was already removed and replaced with a new connection.
	// If so, do nothing.
	x := p.conns[pc.key]
	if x != pc {
		return
	}
	delete(p.conns, pc.key)
}

// DropConnectionsTo removes all pooled connections to the specified host and port.
// This should be called when you know a host is going down (e.g., VM restart or delete)
// to ensure subsequent requests create fresh connections rather than using stale ones.
func (p *Pool) DropConnectionsTo(host string, port int) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	for key, pc := range p.conns {
		if key.host == host && key.port == port {
			delete(p.conns, key)
			if pc.timer != nil {
				pc.timer.Stop()
			}
			if err := pc.client.Close(); err != nil {
				p.log().Warn("error closing SSH connection during drop", "key", key.String(), "error", err)
			}
			p.log().Info("proactively dropped SSH connection", "key", key.String())
		}
	}
}

// Close shuts down the pool and closes all connections immediately.
// Close is idempotent and safe to call multiple times.
func (p *Pool) Close() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.conns == nil {
		return nil // already closed
	}

	var errs []error
	for _, pc := range p.conns {
		pc.mu.Lock()
		if pc.timer != nil {
			pc.timer.Stop()
		}
		pc.mu.Unlock()
		if err := pc.client.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	p.conns = nil

	return errors.Join(errs...)
}
