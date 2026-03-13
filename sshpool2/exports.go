// exports.go defines the stable public API and metrics for sshpool2.
//
// Metrics defined here are part of the package's stability contract:
// their names, label schemas, and semantics must not change without
// a migration path. Internal pool logic may be refactored freely;
// these wrappers ensure callers get apples-to-apples metric continuity.
//
// # Stable metrics
//
//   - sshpool_operation_total (counter, labels: method, result)
//   - sshpool_operation_duration_seconds (histogram, labels: method, result)
//   - sshpool_cache_total (counter, labels: result=hit|miss) — defined in pool.go
//     but included in the stability contract. The miss rate serves as the
//     connection-establishment rate (cache miss = new SSH connection).
//
// # Metric design decisions
//
// Raw counters are emitted, not rates or ratios. Normalization is deferred to
// the query/alerting layer so that a single set of counters supports multiple
// aggregation windows without redeploying.
//
// The result label classifies errors into categories that separate actionable
// pool-health problems from background noise:
//
//   - Pool health (alert numerator): error_stale, error_transport, error_timeout.
//   - Expected noise (excluded from alerts): error_backend_refused (normal
//     backend lifecycle), error_cancelled (caller's choice), error_command
//     (remote command exited non-zero — an application concern, not a pool one).
//   - Unanticipated: error_other — review periodically for new classifiers.
//
// # Recommended alert expressions
//
// Error ratio — primary pool health signal, normalized for request volume:
//
//	  rate(sshpool_operation_total{result=~"error_transport|error_timeout|error_stale"}[5m])
//	/ rate(sshpool_operation_total[5m])
//	> 0.05
//
// Cache miss ratio — connection churn signal (high miss rate = pool not retaining):
//
//	  rate(sshpool_cache_total{result="miss"}[5m])
//	/ rate(sshpool_cache_total[5m])
//	> 0.3
//
// Latency — inherently volume-normalized:
//
//	histogram_quantile(0.99, rate(sshpool_operation_duration_seconds_bucket[5m])) > 2
//
// All three are ratio-based, so they are stable across traffic volume changes,
// time-of-day variation, and host count fluctuation. For cold-start tolerance
// (process restart → empty cache → brief miss spike), use multi-window burn
// rate: require both the 5m and 1h windows to breach before paging.
package sshpool2

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"syscall"
	"time"

	"golang.org/x/crypto/ssh"
)

// ErrStaleConnection indicates that a pooled SSH connection appears
// unresponsive: a channel open (port-forward or session) did not complete
// within staleTimeout. The pool has evicted the connection; callers should
// retry.
//
// Defined in the stable API surface (exports.go) because callers check for
// it with errors.Is. The stale-detection logic that produces this error may
// not be present yet — in that case the error_stale metric label simply
// never fires, and the classifier is forward-compatible for when stale
// detection lands.
var ErrStaleConnection = errors.New("sshpool2: stale connection")

// classifyError categorizes the error returned by pool operations into a
// stable metric label. The categories are chosen to separate actionable
// failure modes from background noise in alerting:
//
//   - error_stale, error_transport, error_timeout: pool health problems
//     (connection churn, dead transports, hung channels, TCP timeouts).
//     These are the primary signals for error-ratio alerts.
//   - error_backend_refused: the SSH transport is healthy but the destination
//     behind the tunnel rejected the connection. This is normal backend
//     lifecycle (deploys, restarts) and is excluded from pool-health alerts
//     to avoid false pages. Uses string matching because the SSH channel-open
//     error wraps the remote TCP error message without a structured sentinel.
//   - error_cancelled: the caller chose to cancel — not a pool problem.
//     Excluded from pool-health alerts.
//   - error_command: the remote command exited non-zero (*ssh.ExitError).
//     Only produced by RunCommand. This is an application-level outcome,
//     not a pool-health problem — the SSH transport worked correctly.
//     Excluded from pool-health alerts.
//   - error_other: catch-all for unanticipated errors. Alerts on this
//     category should be reviewed periodically to see if new classifiers
//     are needed.
//
// Order matters: ErrStaleConnection wraps DeadlineExceeded (via fmt.Errorf
// with %w), so the stale check must precede the timeout check — otherwise
// errors.Is(err, context.DeadlineExceeded) would match first and
// misclassify stale evictions as generic timeouts.
func classifyError(err error) string {
	switch {
	case err == nil:
		return "success"
	case errors.Is(err, ErrStaleConnection):
		return "error_stale"
	case errors.Is(err, context.Canceled):
		return "error_cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		return "error_timeout"
	case strings.Contains(err.Error(), "onnection refused"): // case-insensitive: matches both "Connection refused" (SSH) and "connection refused" (syscall)
		return "error_backend_refused"
	case errors.Is(err, io.EOF),
		errors.Is(err, net.ErrClosed),
		errors.Is(err, syscall.ECONNRESET),
		errors.Is(err, syscall.EPIPE),
		errors.Is(err, syscall.ETIMEDOUT):
		return "error_transport"
	case isExitError(err):
		return "error_command"
	default:
		return "error_other"
	}
}

// isExitError reports whether err wraps an *ssh.ExitError (remote command
// exited non-zero). Factored out so classifyError reads as a flat switch.
func isExitError(err error) bool {
	var exitErr *ssh.ExitError
	return errors.As(err, &exitErr)
}

// recordOp records a single pool operation in Prometheus. Safe to call when
// p.Metrics is nil (no-op).
//
// Emits raw counters and histogram observations — not rates or ratios.
// Normalization is intentionally deferred to the query layer (Prometheus
// recording rules / Grafana) so that a single set of counters supports
// multiple aggregation windows and ratio definitions without re-deploying
// the application. See the file-level comment for the stability contract.
//
// The method label matches the exported Go method name (e.g. "DialContext",
// "RunCommand") so Prometheus queries map directly to API call sites.
func (p *Pool) recordOp(method string, err error, d time.Duration) {
	if p.Metrics == nil {
		return
	}
	result := classifyError(err)
	p.Metrics.operationTotal.WithLabelValues(method, result).Inc()
	p.Metrics.operationDuration.WithLabelValues(method, result).Observe(d.Seconds())
}

// DialContext dials the target address through a pooled SSH connection.
//
// This is a thin metrics wrapper around the internal dialContext implementation.
// Metrics are recorded at this API boundary — not inside pool internals — so
// that internal refactoring cannot break metric continuity. Each call records
// exactly one observation of sshpool_operation_total and
// sshpool_operation_duration_seconds, covering the full connect-through-pool +
// port-forward latency.
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
	start := time.Now()
	conn, err := p.dialContext(ctx, network, addr, host, user, port, signer, config)
	p.recordOp("DialContext", err, time.Since(start))
	return conn, err
}

// DialWithRetries dials with retries on the entire operation (connect + port-forward).
//
// Metrics wrapper: records one observation for the entire retry sequence, not
// per attempt. The metric reflects the caller-visible outcome: if a connection
// was obtained (conn != nil), the result is "success" regardless of errors from
// prior retried attempts (which are joined into the returned error for caller
// diagnostics but don't represent the final outcome). Per-attempt visibility
// comes from cache misses in sshpool_cache_total.
//
// The internal retry loop calls dialContext (not the exported DialContext), so
// retries do not double-count in sshpool_operation_total.
//
// This is safe because dialing is idempotent.
// The returned error may contain errors from prior attempts, even on success.
func (p *Pool) DialWithRetries(ctx context.Context, network, addr, host, user string, port int, signer ssh.Signer, config *ssh.ClientConfig, retries []time.Duration) (net.Conn, error) {
	start := time.Now()
	conn, err := p.dialWithRetries(ctx, network, addr, host, user, port, signer, config, retries)
	// dialWithRetries returns (conn, joinedPriorErrors) on success — conn is
	// non-nil but err contains errors from retried attempts. The metric must
	// reflect the caller-visible outcome (success), not the prior-attempt noise.
	if conn != nil {
		p.recordOp("DialWithRetries", nil, time.Since(start))
	} else {
		p.recordOp("DialWithRetries", err, time.Since(start))
	}
	return conn, err
}

// RunCommand runs a command on a remote host through a pooled SSH connection.
//
// Metrics wrapper: records one observation covering connect (with retries) +
// session creation + command execution. The duration includes connect retries,
// so it reflects caller-visible wall time, not just command execution.
//
// Command-exit failures (non-zero exit status) are classified as error_command,
// not error_other — they indicate the SSH transport worked correctly and the
// remote command produced an application-level failure. This keeps error_other
// clean for genuinely unanticipated errors.
//
// Connection establishment is retried according to connRetries; the command
// itself runs at most once.
// stdin is optional; pass nil if the command doesn't need input.
func (p *Pool) RunCommand(ctx context.Context, host, user string, port int, signer ssh.Signer, config *ssh.ClientConfig, command string, stdin io.Reader, connRetries []time.Duration) ([]byte, error) {
	start := time.Now()
	output, err := p.runCommand(ctx, host, user, port, signer, config, command, stdin, connRetries)
	p.recordOp("RunCommand", err, time.Since(start))
	return output, err
}

// DropConnectionsTo removes all pooled connections to the specified host and port.
// This should be called when you know a host is going down (e.g., VM restart or delete)
// to ensure subsequent requests create fresh connections rather than using stale ones.
//
// No metrics: this is a control-plane operation (called during host lifecycle
// events), not a data-plane operation. Its latency and error rate are not
// meaningful health signals — they'd just add noise to dashboards.
func (p *Pool) DropConnectionsTo(host string, port int) { p.dropConnectionsTo(host, port) }

// Close shuts down the pool and closes all connections immediately.
// Close is idempotent and safe to call multiple times.
//
// No metrics: shutdown is a once-per-lifetime event with no meaningful
// latency or error signal for ongoing health monitoring.
func (p *Pool) Close() error { return p.closePool() }
