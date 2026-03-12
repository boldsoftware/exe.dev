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
	"syscall"
	"time"

	"exe.dev/errorz"
	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/crypto/ssh"
	"tailscale.com/util/singleflight"
)

// Metrics holds Prometheus metrics for the SSH connection pool.
type Metrics struct {
	cacheTotal        *prometheus.CounterVec
	operationTotal    *prometheus.CounterVec   // labels: method, result
	operationDuration *prometheus.HistogramVec // labels: method, result
}

// NewMetrics creates and registers pool metrics.
func NewMetrics(registry *prometheus.Registry) *Metrics {
	opLabels := []string{"method", "result"}
	m := &Metrics{
		cacheTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "sshpool_cache_total",
				Help: "Total number of SSH pool cache lookups.",
			},
			[]string{"result"}, // "hit" or "miss"
		),
		operationTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "sshpool_operation_total",
				Help: "Total number of SSH pool operations.",
			},
			opLabels,
		),
		operationDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name: "sshpool_operation_duration_seconds",
				Help: "Duration of SSH pool operations.",
				// Bucket boundaries are shaped for SSH-through-pool latency to co-located
				// backends: cache hits resolve in <10ms, cache misses (TCP+handshake) in
				// ~50-500ms. The 10ms-100ms range gives resolution on the fast path;
				// 250ms-1s covers cache misses; 2.5s-10s catches pathological retries
				// and RunCommand tails.
				Buckets: []float64{0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
			},
			opLabels,
		),
	}
	registry.MustRegister(m.cacheTotal, m.operationTotal, m.operationDuration)
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
//
// Lock ordering: p.mu must not be held when acquiring pc.mu.
// release() acquires pc.mu then p.mu (via removeConn).
// DropConnectionsTo and Close snapshot under p.mu, release it,
// then operate on each pc.mu individually.
//
// Refcount protocol: each connect() or connected() call increments active.
// Each increment is paired with exactly one decrement — either in
// disconnected() (when timer.Reset stops the old timer) or in release()
// (when the timer fires). When active reaches 0, release() removes the
// pooledConn from the map and closes the client. All three methods
// (removeConn, ssh.Client.Close) are idempotent, so zombie timer firings
// after eviction are harmless.
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
// Callers that receive true must call disconnected exactly once when done
// (directly or via trackedConn.Close).
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
	// When !stopped, either the old timer's release() already fired (and
	// will handle a prior caller's active increment), or the timer was
	// externally stopped by DropConnectionsTo/Close and the pooledConn is
	// orphaned (removed from map, client closed). In both cases, Reset
	// schedules a new release() that will handle *this* caller's increment.
	// Each connected()/disconnected() increment is thus paired with exactly
	// one decrement — either here (when we stop the timer) or in release().
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

	// Remove from pool under lock so no new callers can discover this conn.
	pc.pool.removeConn(pc)
	// Background: Close can block on a stale TCP connection (retransmit
	// timeout). All other eviction paths use go pc.client.Close() for this
	// reason; release() does the same for consistency.
	// No lock needed: pc.client is immutable and Close is goroutine-safe.
	go func() {
		if err := pc.client.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			pc.log.Warn("error closing SSH connection", "key", pc.key.String(), "error", err)
		}
	}()
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

// connectTo returns a pooled connection for the given host, user, port, and signer.
func (p *Pool) connectTo(ctx context.Context, host, user string, port int, signer ssh.Signer, config *ssh.ClientConfig) (*pooledConn, error) {
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
	return pc, nil
}

// retryLoop retries work until success or retries exhausted.
// Returns the result and any errors (may include errors from prior attempts even on success).
func retryLoop[T any](ctx context.Context, retries []time.Duration, work func() (T, error)) (T, error) {
	retries = slices.Clone(retries)
	retries = append(retries, 0) // final attempt has no sleep after it
	var zero T
	var errs []error
	for _, delay := range retries {
		if err := ctx.Err(); err != nil {
			errs = append(errs, err)
			return zero, errors.Join(errs...)
		}

		result, err := work()
		if err == nil {
			return result, errors.Join(errs...)
		}
		errs = append(errs, err)

		select {
		case <-time.After(delay):
		case <-ctx.Done():
		}
	}

	if err := ctx.Err(); err != nil {
		errs = append(errs, err)
	}
	return zero, errors.Join(errs...)
}

// connectToWithRetries returns a pooled connection, retrying on failure.
func (p *Pool) connectToWithRetries(ctx context.Context, host, user string, port int, signer ssh.Signer, config *ssh.ClientConfig, retries []time.Duration) (*pooledConn, error) {
	return retryLoop(ctx, retries, func() (*pooledConn, error) {
		return p.connectTo(ctx, host, user, port, signer, config)
	})
}

// dialContext dials the target address through a pooled SSH connection.
// See DialContext for the exported wrapper.
func (p *Pool) dialContext(ctx context.Context, network, addr, host, user string, port int, signer ssh.Signer, config *ssh.ClientConfig) (net.Conn, error) {
	pc, err := p.connectTo(ctx, host, user, port, signer, config)
	if err != nil {
		return nil, err
	}
	return p.dialThroughClient(ctx, pc, network, addr)
}

// dialWithRetries dials with retries on the entire operation (connect + port-forward).
// See DialWithRetries for the exported wrapper.
func (p *Pool) dialWithRetries(ctx context.Context, network, addr, host, user string, port int, signer ssh.Signer, config *ssh.ClientConfig, retries []time.Duration) (net.Conn, error) {
	return retryLoop(ctx, retries, func() (net.Conn, error) {
		return p.dialContext(ctx, network, addr, host, user, port, signer, config)
	})
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
	deadline := time.Now().Add(3 * time.Second) // fail fast on new connections

	// Establish TCP connection with deadline.
	// Use Deadline (not Timeout) so TCP connect + SSH handshake share the same 3s budget.
	dialer := net.Dialer{Deadline: deadline}
	conn, err := dialer.Dial("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("SSH dial failed: %w", err)
	}

	// Set deadline covering the SSH handshake (version exchange, key exchange, auth).
	// ssh.ClientConfig.Timeout only covers TCP connect, not the handshake.
	if err := conn.SetDeadline(deadline); err != nil {
		conn.Close()
		return nil, fmt.Errorf("failed to set deadline on ssh dialed conn: %w", err)
	}

	sshConn, chans, reqs, err := ssh.NewClientConn(conn, addr, config)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("SSH new client conn failed: %w", err)
	}

	// We're fully connected. Clear deadline for actual future use.
	if err := conn.SetDeadline(time.Time{}); err != nil {
		sshConn.Close()
		return nil, fmt.Errorf("failed to clear deadline: %w", err)
	}

	client := ssh.NewClient(sshConn, chans, reqs)
	p.log().Info("established new SSH connection in pool", "key", key.String())

	pc = &pooledConn{client: client, key: key, pool: p, log: p.log()}
	// Mark as connected, insert into the pool, then disconnect for balance.
	// setConn must precede disconnected so release() can find pc in the map.
	// This starts the TTL clock running.
	// Under normal operation, the connection will be used immediately after this.
	pc.connected()
	p.setConn(pc)
	pc.disconnected()

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

// runCommand runs a command on a remote host through a pooled SSH connection.
// See RunCommand for the exported wrapper.
func (p *Pool) runCommand(ctx context.Context, host, user string, port int, signer ssh.Signer, config *ssh.ClientConfig, command string, stdin io.Reader, connRetries []time.Duration) ([]byte, error) {
	pc, err := p.connectToWithRetries(ctx, host, user, port, signer, config, connRetries)
	if pc == nil {
		return nil, err
	}
	output, cmdErr := p.runCommandOnClient(ctx, pc, command, stdin)
	if cmdErr != nil {
		return output, cmdErr
	}
	return output, nil
}

// runCommandOnClient runs a command through the SSH client.
func (p *Pool) runCommandOnClient(ctx context.Context, pc *pooledConn, command string, stdin io.Reader) ([]byte, error) {
	alive := pc.connect()
	if !alive {
		return nil, fmt.Errorf("runCommandOnClient: SSH connection pool entry is unexpectedly dead")
	}
	defer pc.disconnected() // balance the connect() call

	session, err := pc.client.NewSession()
	if err != nil {
		if isSSHConnError(err) {
			p.log().InfoContext(ctx, "dropping dead ssh connection", "key", pc.key.String(), "err", err)
			p.removeConn(pc)
		}
		return nil, fmt.Errorf("failed to create session: %w", err)
	}
	defer session.Close()

	if stdin != nil {
		session.Stdin = stdin
	}
	output, err := session.CombinedOutput(command)
	if err != nil {
		// Check if the error indicates a dead connection
		if isSSHConnError(err) {
			p.log().InfoContext(ctx, "dropping dead ssh connection after command", "key", pc.key.String(), "err", err)
			p.removeConn(pc)
		}
		return output, fmt.Errorf("command failed: %w", err)
	}
	return output, nil
}

func isSSHConnError(err error) bool {
	if errorz.HasType[*ssh.OpenChannelError](err) {
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
	// TCP-level errors indicate the underlying connection is dead.
	// ECONNRESET: "connection reset by peer" - peer sent RST
	// EPIPE: "broken pipe" - writing to a closed connection
	if errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE) {
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

// dropConnectionsTo removes all pooled connections to the specified host and port.
// See DropConnectionsTo for the exported wrapper.
//
// Best-effort: a concurrent connect() can insert a new pooledConn between
// the snapshot under p.mu and the per-pc teardown loop. This is harmless —
// the new connection is healthy and will be used normally.
func (p *Pool) dropConnectionsTo(host string, port int) {
	if p == nil {
		return
	}

	// Collect matching connections under p.mu, then release p.mu before
	// touching pc.mu. This avoids a lock-order inversion with release(),
	// which acquires pc.mu → p.mu (via removeConn).
	type dropped struct {
		key connKey
		pc  *pooledConn
	}
	var matches []dropped
	p.mu.Lock()
	for key, pc := range p.conns {
		if key.host == host && key.port == port {
			delete(p.conns, key)
			matches = append(matches, dropped{key, pc})
		}
	}
	p.mu.Unlock()

	for _, m := range matches {
		m.pc.mu.Lock()
		if m.pc.timer != nil {
			m.pc.timer.Stop()
			// Stopping the timer prevents release() from firing, so active
			// is never decremented for the stopped timer's refcount. This is
			// harmless: the pooledConn is already removed from the map and
			// client.Close() runs below, so the orphaned pooledConn is GC'd
			// normally. Any active trackedConn holders will call disconnected(),
			// which restarts the timer and eventually drains active to 0
			// (up to active * TTL timer fires to fully drain).
		}
		m.pc.mu.Unlock()
		// Background: Close can block on a stale TCP retransmit timeout,
		// and DropConnectionsTo is called precisely when a host is going
		// down — the scenario most likely to have stale TCP. Async close
		// matches all other eviction paths (release, dialThroughClient,
		// runCommandOnClient). Errors are fire-and-forget (logged only).
		go func() {
			if err := m.pc.client.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
				p.log().Warn("error closing SSH connection during drop", "key", m.key.String(), "error", err)
			}
		}()
		p.log().Info("proactively dropped SSH connection", "key", m.key.String())
	}
}

// closePool shuts down the pool and closes all connections immediately.
// See Close for the exported wrapper.
func (p *Pool) closePool() error {
	if p == nil {
		return nil
	}

	// Snapshot and nil-out under p.mu, then release p.mu before
	// touching pc.mu. Same lock-order concern as DropConnectionsTo.
	p.mu.Lock()
	conns := p.conns
	p.conns = nil
	p.mu.Unlock()

	if conns == nil {
		return nil // already closed
	}

	// Close connections sequentially to collect errors.
	// Unlike hot-path eviction (which backgrounds Close to avoid blocking
	// callers on stale TCP retransmit timeouts), Close() blocks intentionally:
	// shutdown callers expect blocking semantics and need error aggregation.
	// With N stale connections this can take N × TCP retransmit timeout.
	var errs []error
	for _, pc := range conns {
		pc.mu.Lock()
		if pc.timer != nil {
			pc.timer.Stop() // see DropConnectionsTo comment re: stranded active
		}
		pc.mu.Unlock()
		if err := pc.client.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			errs = append(errs, err)
		}
	}

	return errors.Join(errs...)
}
