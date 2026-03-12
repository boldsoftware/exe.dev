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
				// ~50-500ms, and staleTimeout fires at 500ms. The 10ms-100ms range gives
				// resolution on the fast path; 250ms-1s covers cache misses and stale
				// detection; 2.5s-10s catches pathological retries and RunCommand tails.
				Buckets: []float64{0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
			},
			opLabels,
		),
	}
	registry.MustRegister(m.cacheTotal, m.operationTotal, m.operationDuration)
	return m
}

// staleTimeout is the short timeout used to detect stale or half-open SSH
// connections. When a channel open (port-forward dial or session creation)
// doesn't complete within this duration, the pool treats the connection as
// stale and evicts it.
//
// This timeout serves double duty: it detects genuinely stale SSH transports
// (where the channel open hangs because the remote end is gone) AND slow
// destinations behind a healthy SSH tunnel (where TCP connect to the backend
// takes too long). The pool cannot distinguish these cases — both look like
// "channel open didn't complete in time." For co-located backends where TCP
// connect + channel open should take <10ms, 500ms is very generous; false
// positives (evicting a healthy SSH connection because the backend was slow)
// should be rare. When they do occur, over-evicting is the safer failure mode:
// a stale connection causes repeated failures, while a spurious eviction just
// costs one SSH re-establishment.
//
// Callers should set their context deadlines well above this value
// (at least 2x, i.e. 1s at the current setting).
// If a caller's deadline is shorter than staleTimeout, the pool cannot
// distinguish "caller ran out of time" from "connection is stale" and
// conservatively keeps the connection (see shortTimeoutIsOurs).
const staleTimeout = 500 * time.Millisecond

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

// activeCount returns the current active refcount. Safe for concurrent use.
func (pc *pooledConn) activeCount() int {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	return pc.active
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

	// OnConnClosed is called when a pooled SSH connection dies.
	// The callback fires asynchronously after ssh.Client.Wait returns,
	// which means the SSH connection's channels have already been closed
	// (pending buffers EOF'd, writes return io.EOF). Callers can use this
	// to flush HTTP idle connections that were riding on the dead tunnel.
	//
	// Must be set before the first use of the Pool.
	OnConnClosed func(host, user string, port int, publicKey string)

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
// DoChan lets each caller return on its own ctx.Done() while ensuring
// connection establishment always completes (warming the pool for future callers).
func (p *Pool) connectTo(ctx context.Context, host, user string, port int, signer ssh.Signer, config *ssh.ClientConfig) (*pooledConn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key := connKey{
		host:      host,
		user:      user,
		port:      port,
		publicKey: string(signer.PublicKey().Marshal()),
	}
	// Do not pass the caller's context into the singleflight function:
	// even if all current callers cancel, the connection should still be
	// established so future callers can reuse it. DoChan (not DoChanContext)
	// gives us non-blocking waits without injecting a cancellable context
	// into the work function.
	ch := p.sfGroup.DoChan(key, func() (*pooledConn, error) {
		return p.connect(key, config)
	})
	select {
	case res := <-ch:
		if res.Err != nil {
			// select may also pick this case when ctx is done.
			// Prefer the caller's context error when available,
			// so direct DialContext callers see a predictable error.
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			return nil, res.Err
		}
		// select may pick this case even if ctx is also done;
		// bail early rather than handing a good conn to a cancelled caller.
		if ctx.Err() != nil {
			// Safe to discard res.Val: connect() already stored the conn
			// in the pool via setConn() and balanced its own refcount
			// (connected/disconnected), so no cleanup is needed here.
			return nil, ctx.Err()
		}
		return res.Val, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
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

	// Watch for SSH connection death. When mux.loop() exits, all channels
	// have already been closed (pending buffers EOF'd, writes return io.EOF).
	// We proactively remove the dead connection from the pool and notify the
	// caller so it can flush HTTP idle connections that rode on this tunnel.
	go func() {
		client.Wait()
		if p.removeConn(pc) {
			p.log().Info("SSH connection closed, removing from pool", "key", key.String())
			if p.OnConnClosed != nil {
				p.OnConnClosed(key.host, key.user, key.port, key.publicKey)
			}
		}
	}()

	return pc, nil
}

// dialThroughClient dials through the SSH client and wraps the connection
func (p *Pool) dialThroughClient(ctx context.Context, pc *pooledConn, network, addr string) (net.Conn, error) {
	// If the caller's context is already done, return immediately.
	// Avoids a wasted dial through a shortCtx that would inherit cancellation.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	alive := pc.connect()
	if !alive {
		// Should only happen if there was a very short TTL.
		// This caller should be retrying anyway.
		return nil, fmt.Errorf("dialThroughClient: SSH connection pool entry is unexpectedly dead, is the TTL set low?")
	}
	// Use a short timeout to detect stale/half-open SSH connections.
	// "Port not bound" still fails fast (connection refused is immediate).
	// Stale-detection logic here mirrors runCommandOnClient — keep in sync.
	// Primary stale-detection path: DialContext observes shortCtx, so stale
	// connections surface as DeadlineExceeded in the "case res" branch below.
	shortCtx, cancel := context.WithTimeout(ctx, staleTimeout)
	defer cancel()

	// Determine whether staleTimeout is genuinely ours or was inherited from
	// a shorter caller deadline. context.WithTimeout picks min(parent, timeout),
	// so if ctx's deadline is shorter than staleTimeout, shortCtx inherits it.
	// We compute this once from immutable deadline values to avoid a TOCTOU race:
	// checking ctx.Err() at decision time is racy because the caller's context
	// may expire between shortCtx firing and our check.
	shortTimeoutIsOurs := shortTimeoutOwnership(ctx, shortCtx)

	conn, err := pc.client.DialContext(shortCtx, network, addr)
	if err != nil {
		p.log().InfoContext(ctx, "dial failed", "err", err, "errtype", reflect.TypeOf(err))
		// Error classification priority:
		// 1. Transport errors (EOF, ECONNRESET, etc.) — connection is dead, always evict.
		// 2. Stale detection — our staleTimeout fired (not inherited from caller)
		//    AND the caller didn't explicitly cancel. The connection might be
		//    half-open or the backend might be slow; we can't distinguish, so
		//    we evict conservatively (see staleTimeout doc).
		// 3. Everything else (caller cancelled, caller's own deadline, unknown
		//    errors) — don't evict. The connection may be healthy.
		//
		// Why ctx.Err() != context.Canceled is safe here despite the TOCTOU
		// concern with ctx.Err() == nil: explicit cancellation is a
		// happens-before relationship — the parent's cancel() runs before the
		// child's Done channel closes, so ctx.Err() == context.Canceled is
		// guaranteed stable when we reach this code. The TOCTOU only applies
		// to deadline-based expiry, which shortTimeoutIsOurs handles.
		switch {
		case isSSHConnError(err):
			p.log().InfoContext(ctx, "dropping dead ssh connection", "key", pc.key.String(), "err", err)
			p.removeConn(pc)
			// Eager close: for transport-dead errors (EOF, ECONNRESET), sibling
			// channels are already doomed. For ResourceShortage the transport
			// may be alive, but the connection is low-value under resource
			// pressure — retry backoff absorbs the churn. Prior code only
			// called removeConn here; the explicit Close frees resources
			// immediately instead of waiting for the TTL timer. release()
			// will harmlessly re-close.
			// No lock needed: pc.client is immutable and Close is goroutine-safe.
			// Background: Close can block on a stale TCP connection (retransmit
			// timeout). The pooledConn is already evicted, so there's no reason
			// to make the caller wait.
			go pc.client.Close()
			// Falls through: eviction occurred but ErrStaleConnection is intentionally
			// not wrapped — transport errors are self-describing. See ErrStaleConnection doc.
		case shortCtx.Err() != nil && shortTimeoutIsOurs && ctx.Err() != context.Canceled:
			// Note: a caller cancel arriving between stale timeout firing and this
			// classification would suppress eviction (ctx.Err() == Canceled). This race
			// is intentionally resolved in favor of non-eviction — the next caller will
			// re-detect staleness.
			//
			// Co-firing edge: a non-timeout error (e.g. application-level rejection)
			// arriving at the exact moment shortCtx expires would be misclassified as
			// stale. The window is nanosecond-scale and the failure mode is conservative
			// (over-eviction), so we accept it rather than gating on error type — the
			// underlying error is not guaranteed to be DeadlineExceeded.
			p.log().InfoContext(ctx, "dropping stale ssh connection", "key", pc.key.String(), "active", pc.activeCount(), "err", err)
			p.removeConn(pc)
			// Eager close: tears down the SSH transport immediately, including
			// any other multiplexed channels on this connection. This is a
			// deliberate choice — for co-located backends where staleTimeout
			// false positives are rare, fast failover outweighs the risk of
			// collateral failures from a misdiagnosis. See staleTimeout doc.
			// Background: Close can block on a stale TCP retransmit timeout;
			// the pooledConn is already evicted so the caller need not wait.
			go pc.client.Close()
			err = fmt.Errorf("channel open did not complete within %s: %w: %w", staleTimeout, ErrStaleConnection, err)
		case shortCtx.Err() != nil && shortTimeoutIsOurs && ctx.Err() == context.Canceled:
			p.log().DebugContext(ctx, "stale detection suppressed: concurrent caller cancellation", "key", pc.key.String(), "err", err)
		case shortCtx.Err() != nil && !shortTimeoutIsOurs:
			p.log().DebugContext(ctx, "stale detection suppressed: caller deadline shorter than staleTimeout", "key", pc.key.String(), "err", err)
		}
		// Balance the connect() call. On eviction paths above, removeConn
		// ran and client.Close was launched asynchronously, so disconnected()
		// creates a zombie timer that fires into release(), which decrements
		// the refcount and calls Close/removeConn (both idempotent). The
		// pooledConn lingers for up to active × TTL cycles; not worth
		// restructuring the refcount protocol to avoid.
		pc.disconnected()
		return nil, fmt.Errorf("failed to dial %s via SSH: %w", addr, err)
	}
	// Set up a tracked connection that calls pc.disconnected() when conn closes.
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
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	alive := pc.connect()
	if !alive {
		return nil, fmt.Errorf("runCommandOnClient: SSH connection pool entry is unexpectedly dead")
	}
	// Balance the connect() call. On eviction paths below, this creates a
	// zombie timer (see dialThroughClient comment); harmless, bounded by active × TTL.
	defer pc.disconnected()

	// NewSession doesn't accept a context, so we run it in a goroutine
	// and use a short timeout to detect stale/half-open SSH connections.
	// Stale-detection logic here mirrors dialThroughClient — keep in sync.
	// Primary stale-detection path: NewSession doesn't observe shortCtx, so
	// stale connections surface in "case <-shortCtx.Done()" below (not "case res").
	type sessionResult struct {
		session *ssh.Session
		err     error
	}
	ch := make(chan sessionResult, 1)
	go func() {
		s, err := pc.client.NewSession()
		ch <- sessionResult{s, err}
	}()

	shortCtx, cancel := context.WithTimeout(ctx, staleTimeout)
	defer cancel()
	shortTimeoutIsOurs := shortTimeoutOwnership(ctx, shortCtx)

	var session *ssh.Session
	select {
	case res := <-ch:
		if res.err != nil {
			// Same classification as dialThroughClient (see comments there):
			// transport errors → evict, stale timeout (ours, not cancelled) →
			// evict + ErrStaleConnection, else → keep.
			//
			// The stale-timeout case below is defensive: since NewSession
			// doesn't observe shortCtx, the only way it fires is if
			// NewSession returns a non-transport error at the exact moment
			// shortCtx expires and the runtime selects this case. Practically
			// unreachable, but harmless (conservative eviction).
			switch {
			case isSSHConnError(res.err):
				p.log().InfoContext(ctx, "dropping dead ssh connection", "key", pc.key.String(), "err", res.err)
				p.removeConn(pc)
				// Eager close: see dialThroughClient comment. For ResourceShortage
				// the transport may be alive, but low-value under resource pressure.
				// Background: see dialThroughClient comment re: stale TCP blocking.
				go pc.client.Close()
				// Falls through: eviction occurred but ErrStaleConnection is intentionally
				// not wrapped — transport errors are self-describing. See ErrStaleConnection doc.
			case shortCtx.Err() != nil && shortTimeoutIsOurs && ctx.Err() != context.Canceled:
				p.log().InfoContext(ctx, "dropping stale ssh connection", "key", pc.key.String(), "active", pc.activeCount(), "err", res.err)
				p.removeConn(pc)
				go pc.client.Close() // eager close, see dialThroughClient stale-eviction comment
				// Returns directly (unlike dialThroughClient which reassigns err
				// and falls through to the common wrapper). The early return is
				// intentional: this branch is practically unreachable (see comment
				// above) and keeping it self-contained is clearer than emulating
				// dialThroughClient's fallthrough pattern.
				return nil, fmt.Errorf("session creation did not complete within %s: %w: %w", staleTimeout, ErrStaleConnection, res.err)
			}
			return nil, fmt.Errorf("failed to create session: %w", res.err)
		}
		session = res.session
	case <-shortCtx.Done():
		if !shortTimeoutIsOurs || ctx.Err() == context.Canceled {
			// Either the caller's own deadline expired (inherited by shortCtx)
			// or the caller explicitly cancelled. Not a stale connection.
			if !shortTimeoutIsOurs {
				p.log().DebugContext(ctx, "stale detection suppressed: caller deadline shorter than staleTimeout", "key", pc.key.String())
			} else {
				p.log().DebugContext(ctx, "stale detection suppressed: concurrent caller cancellation", "key", pc.key.String())
			}
			// Drain the in-flight NewSession goroutine to avoid leaking
			// a remote session channel. This goroutine blocks until NewSession
			// completes; if the connection is stale, that happens when
			// release() closes pc.client after the last active user
			// disconnects and the TTL expires — so the goroutine's lifetime
			// is bounded by last-active-user-disconnect + TTL.
			//
			// Under sustained caller throughput with short deadlines against a
			// genuinely stale connection, drain goroutines accumulate at the
			// caller arrival rate: each caller's defer pc.disconnected() resets
			// the TTL timer, so the TTL never fires and pc.client.Close() never
			// unblocks the hung NewSession calls. The TTL/caller_deadline bound
			// only kicks in after an inactivity gap long enough for the TTL to
			// expire. Manageable in practice (requires all callers to use
			// deadlines shorter than staleTimeout) but worth noting for services
			// with very short deadlines and high call rates.
			// TODO(#85): add observability (slog.Warn or gauge) for drain goroutine accumulation.
			go func() {
				if res := <-ch; res.session != nil {
					res.session.Close()
				}
			}()
			// ctx.Err() is non-nil here: the guard above requires either
			// !shortTimeoutIsOurs (caller deadline fired → DeadlineExceeded)
			// or ctx.Err() == Canceled. The fallback is defensive.
			err := ctx.Err()
			if err == nil {
				err = shortCtx.Err()
			}
			return nil, fmt.Errorf("session creation cancelled: %w", err)
		}
		// Our staleTimeout fired — the SSH connection is unresponsive, evict it.
		p.log().InfoContext(ctx, "dropping stale ssh connection", "key", pc.key.String(), "active", pc.activeCount(), "err", shortCtx.Err())
		p.removeConn(pc)
		// Eager close: see dialThroughClient comment. Background to avoid
		// blocking the caller on a stale TCP retransmit timeout. This also
		// unblocks the hung NewSession goroutine (drain below).
		go pc.client.Close()
		// Clean up the in-flight NewSession goroutine: if it eventually
		// returns a session, close it to avoid resource leaks.
		go func() {
			if res := <-ch; res.session != nil {
				res.session.Close()
			}
		}()
		return nil, fmt.Errorf("session creation did not complete within %s: %w: %w", staleTimeout, ErrStaleConnection, shortCtx.Err())
	}
	defer session.Close()

	// select may pick the session-ready case even if ctx is also done;
	// bail early rather than sending a command the caller won't read.
	if ctx.Err() != nil {
		return nil, fmt.Errorf("session ready but context done: %w", ctx.Err())
	}

	if stdin != nil {
		session.Stdin = stdin
	}
	output, err := session.CombinedOutput(command) // TODO(#84): wrap in goroutine+select for ctx cancellation
	if err != nil {
		if isSSHConnError(err) {
			p.log().InfoContext(ctx, "dropping dead ssh connection after command", "key", pc.key.String(), "err", err)
			p.removeConn(pc)
			// Eager close: see dialThroughClient comment. For ResourceShortage
			// the transport may be alive, but low-value under resource pressure.
			// Background: see dialThroughClient comment re: stale TCP blocking.
			go pc.client.Close()
		}
		return output, fmt.Errorf("command failed: %w", err)
	}
	return output, nil
}

// shortTimeoutOwnership reports whether shortCtx's deadline came from
// our staleTimeout rather than being inherited from a shorter parent deadline.
//
// context.WithTimeout(ctx, staleTimeout) picks min(ctx.Deadline, now+staleTimeout).
// If the caller's context has a deadline shorter than staleTimeout, shortCtx
// inherits it. In that case, a timeout does NOT indicate a stale connection —
// it means the caller ran out of time — and we must not evict a potentially
// healthy connection.
//
// We compare deadlines (immutable values set at creation) rather than checking
// ctx.Err() at decision time, which would be a TOCTOU race: the caller's
// context might expire between shortCtx firing and our error-handling code
// running, causing us to misclassify a genuine stale detection as "caller
// cancelled" and skip eviction.
//
// This function only handles deadline-based disambiguation. Callers must
// also check ctx.Err() != context.Canceled to handle explicit cancellation.
// Unlike deadline expiry, explicit cancellation is a happens-before
// relationship (parent cancel propagates before child Done closes), so
// checking ctx.Err() for cancellation is safe.
func shortTimeoutOwnership(ctx, shortCtx context.Context) bool {
	shortDeadline, ok := shortCtx.Deadline()
	if !ok {
		return false // no deadline at all — shouldn't happen, but be safe
	}
	ctxDeadline, hasDeadline := ctx.Deadline()
	if !hasDeadline {
		return true // caller has no deadline, so staleTimeout is definitely ours
	}
	return shortDeadline.Before(ctxDeadline)
}

func isSSHConnError(err error) bool {
	var openErr *ssh.OpenChannelError
	if errors.As(err, &openErr) {
		// Only treat transport-level failures as connection errors.
		// Prohibited, UnknownChannelType, and ConnectionFailed mean the
		// server is alive. ConnectionFailed (RFC 4254 §7.2:
		// SSH_OPEN_CONNECT_FAILED) means the server couldn't reach the
		// backend target — the SSH transport itself is healthy.
		// ResourceShortage is borderline but suggests the transport may
		// be degraded and a fresh connection could help. Note: a server-wide
		// MaxSessions limit can also trigger ResourceShortage, where reconnecting
		// won't help — caller retry backoff absorbs the churn.
		return openErr.Reason == ssh.ResourceShortage
	}
	if errors.Is(err, net.ErrClosed) {
		return true
	}
	if errors.Is(err, io.EOF) {
		return true
	}
	// When the underlying SSH transport has closed, the next channel open often fails
	// with a formatted error that doesn't wrap a well-known sentinel.
	// TODO: upstream a sentinel error to x/crypto/ssh so we don't rely on string matching.
	if strings.Contains(err.Error(), "ssh: unexpected packet in response to channel open") {
		return true
	}
	// Context errors (DeadlineExceeded, Canceled) are NOT classified here.
	// They are ambiguous: the timeout may be from the caller's context or from
	// an internal staleTimeout detecting a stale connection. Call sites
	// disambiguate using shortTimeoutOwnership (see dialThroughClient,
	// runCommandOnClient).
	//
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
func (p *Pool) removeConn(pc *pooledConn) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.conns == nil {
		return false
	}
	// It is possible (albeit unlikely) that pc was already removed and replaced with a new connection.
	// If so, do nothing.
	x := p.conns[pc.key]
	if x != pc {
		return false
	}
	delete(p.conns, pc.key)
	return true
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
