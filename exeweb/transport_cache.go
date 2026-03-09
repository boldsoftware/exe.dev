package exeweb

import (
	"net/http"
	"sync"
	"time"

	"tailscale.com/util/singleflight"
)

// transportKey uniquely identifies an SSH tunnel endpoint for transport caching.
type transportKey struct {
	sshHost   string
	sshUser   string
	sshPort   int
	boxName   string
	publicKey string // marshaled SSH public key
}

type cachedTransport struct {
	t         *http.Transport
	createdAt time.Time
}

// TransportCache pools http.Transport instances per SSH tunnel endpoint.
// This avoids creating a new transport (and discarding its idle connection pool)
// on every proxied request. A background goroutine evicts entries older than ttl.
//
// We need one transport per box rather than a single shared transport because
// http.Transport pools connections by destination address, and all boxes appear
// as 127.0.0.1 through SSH tunneling. A shared transport would reuse box A's
// SSH-tunneled connection for requests to box B. Passing box info via
// context.Context wouldn't help: the transport picks from the pool before
// calling DialContext.
type TransportCache struct {
	mu         sync.Mutex
	transports map[transportKey]cachedTransport
	sf         singleflight.Group[transportKey, *http.Transport]
	ttl        time.Duration
	done       chan struct{}
	closeOnce  sync.Once
}

// NewTransportCache returns a ready-to-use TransportCache.
// A background goroutine evicts entries older than ttl.
func NewTransportCache(ttl time.Duration) *TransportCache {
	tc := &TransportCache{
		transports: make(map[transportKey]cachedTransport),
		ttl:        ttl,
		done:       make(chan struct{}),
	}
	go tc.evictLoop()
	return tc
}

func (tc *TransportCache) evictLoop() {
	ticker := time.NewTicker(tc.ttl / 2)
	defer ticker.Stop()
	for {
		select {
		case <-tc.done:
			return
		case <-ticker.C:
			tc.evict()
		}
	}
}

func (tc *TransportCache) evict() {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	for key, ct := range tc.transports {
		if time.Since(ct.createdAt) >= tc.ttl {
			ct.t.CloseIdleConnections()
			delete(tc.transports, key)
		}
	}
}

// GetOrCreate returns a cached transport for the given key,
// calling create to make one if none exists.
// Concurrent calls for the same key coalesce via singleflight.
func (tc *TransportCache) GetOrCreate(key transportKey, create func() *http.Transport) *http.Transport {
	tc.mu.Lock()
	if ct, ok := tc.transports[key]; ok {
		tc.mu.Unlock()
		return ct.t
	}
	tc.mu.Unlock()

	t, _, _ := tc.sf.Do(key, func() (*http.Transport, error) {
		t := create()
		tc.mu.Lock()
		tc.transports[key] = cachedTransport{t: t, createdAt: time.Now()}
		tc.mu.Unlock()
		return t, nil
	})
	return t
}

// Close stops the eviction goroutine and closes all cached transports.
// It is safe to call Close multiple times; only the first call has any effect.
func (tc *TransportCache) Close() {
	tc.closeOnce.Do(func() {
		close(tc.done)
		tc.mu.Lock()
		defer tc.mu.Unlock()
		for _, ct := range tc.transports {
			ct.t.CloseIdleConnections()
		}
		clear(tc.transports)
	})
}
