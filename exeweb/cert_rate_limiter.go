package exeweb

import (
	"fmt"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// CertRateLimiter limits TLS certificate issuance attempts per VM.
// Each VM gets a token bucket with a maximum of [limit] tokens,
// refilled at [limit] tokens per day. This prevents subdomain
// enumeration attacks from exhausting Let's Encrypt rate limits
// when customers have wildcard DNS pointing at exe.dev infrastructure.
type CertRateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*certBucket
	limit   int              // max tokens (bucket size)
	rate    float64          // tokens per second
	nowFunc func() time.Time // for testing

	// Prometheus metrics (nil-safe; no-op if not registered).
	allowedTotal     *prometheus.CounterVec
	rateLimitedTotal *prometheus.CounterVec
}

type certBucket struct {
	tokens      float64
	lastRefresh time.Time
}

// NewCertRateLimiter creates a rate limiter allowing limit cert
// issuance attempts per VM per day, with burst up to limit.
func NewCertRateLimiter(limit int) *CertRateLimiter {
	return &CertRateLimiter{
		buckets: make(map[string]*certBucket),
		limit:   limit,
		rate:    float64(limit) / (24 * 60 * 60), // tokens per second
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

func (r *CertRateLimiter) now() time.Time {
	if r.nowFunc != nil {
		return r.nowFunc()
	}
	return time.Now()
}

// Allow checks whether a cert issuance attempt for the given VM
// should be allowed. If allowed, it consumes one token and returns nil.
// If the bucket is empty, it returns an error.
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

	// Refill tokens based on elapsed time.
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

// Cleanup removes entries that have been fully refilled (idle for >= 1 day).
// Call periodically to prevent unbounded memory growth.
func (r *CertRateLimiter) Cleanup() {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := r.now()
	for name, b := range r.buckets {
		// If enough time has passed to fully refill, the entry is idle.
		elapsed := now.Sub(b.lastRefresh).Seconds()
		if b.tokens+elapsed*r.rate >= float64(r.limit) {
			delete(r.buckets, name)
		}
	}
}
