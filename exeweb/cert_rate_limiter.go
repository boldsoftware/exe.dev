package exeweb

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/crypto/acme/autocert"
)

// CertRateLimiter limits TLS certificate issuance per VM using a token
// bucket. Each VM gets a bucket of [limit] tokens refilled at [limit]
// tokens per day. It doubles as an autocert HostPolicy: it validates the
// host, checks the cert cache, and only consumes a token on a cache miss.
//
// In observe-only mode (the default), exceeding the limit logs a warning
// but does not block issuance.
type CertRateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*certBucket
	limit   int
	rate    float64 // tokens per second
	nowFunc func() time.Time

	// ValidateHost validates the host and returns the resolved box name.
	// Empty box name means first-party domain (not rate-limited).
	ValidateHost func(ctx context.Context, host string) (boxName string, err error)

	// Cache is the autocert cert cache. Used to skip rate limiting
	// for already-cached certs.
	Cache autocert.Cache

	// Lg is the logger for dry-run warnings.
	Lg *slog.Logger

	allowedTotal     *prometheus.CounterVec
	rateLimitedTotal *prometheus.CounterVec
}

type certBucket struct {
	tokens      float64
	lastRefresh time.Time
}

// NewCertRateLimiter creates a rate limiter that allows limit certificate
// issuances per VM per 24-hour rolling window.
func NewCertRateLimiter(limit int) *CertRateLimiter {
	return &CertRateLimiter{
		buckets: make(map[string]*certBucket),
		limit:   limit,
		rate:    float64(limit) / (24 * 60 * 60),
	}
}

// RegisterMetrics registers Prometheus metrics for the rate limiter.
func (r *CertRateLimiter) RegisterMetrics(registry *prometheus.Registry) {
	r.allowedTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "cert_ratelimit_allowed_total",
			Help: "Total certificate issuance attempts allowed by the rate limiter.",
		},
		[]string{"vm"},
	)
	r.rateLimitedTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "cert_ratelimit_rejected_total",
			Help: "Total certificate issuance attempts rejected by the rate limiter.",
		},
		[]string{"vm"},
	)
	registry.MustRegister(r.allowedTotal, r.rateLimitedTotal)
}

// SetNowFunc overrides the clock used by the rate limiter (for testing).
func (r *CertRateLimiter) SetNowFunc(fn func() time.Time) {
	r.nowFunc = fn
}

func (r *CertRateLimiter) now() time.Time {
	if r.nowFunc != nil {
		return r.nowFunc()
	}
	return time.Now()
}

// HostPolicy is an autocert.HostPolicy that validates the host, checks
// the cert cache, and rate-limits only on cache misses. Rate limiting is
// per VM (box name), so wildcard DNS attacks that generate many subdomains
// for the same box are properly throttled. In observe-only / dry-run mode,
// exceeding the rate limit logs a warning but does not block issuance.
func (r *CertRateLimiter) HostPolicy(ctx context.Context, host string) error {
	boxName, err := r.ValidateHost(ctx, host)
	if err != nil {
		return err
	}

	// First-party domains (WebHost, BoxHost, exe.new, bold.dev) return
	// an empty boxName and are not rate-limited.
	if boxName == "" {
		return nil
	}

	// Check whether the cert is already cached. autocert stores certs
	// under the bare domain name (ECDSA) or domain+"+rsa". A hit on
	// the plain key means we have a cached cert — skip the rate limiter.
	domain := strings.TrimSuffix(host, ".")
	if _, err := r.Cache.Get(ctx, domain); err == nil {
		return nil
	}

	// Cache miss — this will be a new cert issuance. Rate-limit by box
	// name so all custom domains pointing at the same VM share one bucket.
	if err := r.Allow(boxName); err != nil {
		// Observe-only mode: log but do NOT block.
		r.Lg.WarnContext(ctx, "cert rate limiter would block issuance (dry run)",
			"host", host,
			"boxName", boxName,
			"error", err,
		)
	}

	return nil
}

// Allow checks whether a certificate issuance for the given VM should be
// allowed. It returns nil if the request is within the rate limit, or an
// error describing the rejection.
func (r *CertRateLimiter) Allow(vmName string) error {
	if r.limit <= 0 {
		return fmt.Errorf("cert issuance disabled (limit=%d)", r.limit)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	b := r.buckets[vmName]
	if b == nil {
		b = &certBucket{tokens: float64(r.limit), lastRefresh: now}
		r.buckets[vmName] = b
	}
	elapsed := now.Sub(b.lastRefresh).Seconds()
	if elapsed > 0 {
		b.tokens += elapsed * r.rate
		if b.tokens > float64(r.limit) {
			b.tokens = float64(r.limit)
		}
		b.lastRefresh = now
	}
	if b.tokens < 1 {
		if r.rateLimitedTotal != nil {
			r.rateLimitedTotal.WithLabelValues(vmName).Inc()
		}
		return fmt.Errorf("cert issuance rate limit exceeded for VM %s (%.0f/%d tokens)", vmName, b.tokens, r.limit)
	}
	b.tokens--
	if r.allowedTotal != nil {
		r.allowedTotal.WithLabelValues(vmName).Inc()
	}
	return nil
}

// Cleanup removes buckets that have fully replenished their tokens,
// freeing memory for VMs that have been idle.
func (r *CertRateLimiter) Cleanup() {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	for name, b := range r.buckets {
		elapsed := now.Sub(b.lastRefresh).Seconds()
		if b.tokens+elapsed*r.rate >= float64(r.limit) {
			delete(r.buckets, name)
		}
	}
}
