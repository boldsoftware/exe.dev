package exeprox

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"exe.dev/wildcardcert"
	"tailscale.com/util/singleflight"
)

// certCacheKey returns a disk cache key for a TLS certificate.
//
// Autocert selects ECDSA or RSA certs based on the client's
// SignatureSchemes, SupportedCurves, and CipherSuites (via an unexported
// supportsECDSA function). Rather than reimplement that logic and risk
// diverging from autocert, we hash those same fields to produce a cache
// key. Clients with identical TLS capabilities share a cache entry;
// clients with different capabilities get separate entries and autocert
// picks the right cert type for each.
//
// The tradeoff is a few extra disk entries per domain — one per distinct
// client TLS fingerprint rather than the theoretical minimum of two
// (ECDSA + RSA). In practice there are only a handful of common
// fingerprints (major browsers, curl, Go, etc.), so this is negligible.
//
// Only the three fields that autocert uses for ECDSA-vs-RSA selection are
// hashed. TopLevelCert sends additional hello fields to exed (SupportedPoints,
// SupportedProtos, SupportedVersions, Extensions), but exed uses the same
// autocert logic, so those fields don't affect which cert is returned.
// Singleflight may therefore use one caller's hello for another with the
// same cache key; this is fine because clients with identical cipher/curve/
// signature sets have identical remaining fields in practice.
//
// Pass nil for hello when the cert type is fixed (e.g. wildcard certs).
func certCacheKey(serverName string, hello *tls.ClientHelloInfo) string {
	if hello == nil {
		return serverName
	}
	h := sha256.New()
	h.Write([]byte("v1")) // version prefix for future cache busting
	binary.Write(h, binary.LittleEndian, uint32(len(hello.SignatureSchemes)))
	binary.Write(h, binary.LittleEndian, hello.SignatureSchemes)
	binary.Write(h, binary.LittleEndian, uint32(len(hello.SupportedCurves)))
	binary.Write(h, binary.LittleEndian, hello.SupportedCurves)
	binary.Write(h, binary.LittleEndian, uint32(len(hello.CipherSuites)))
	binary.Write(h, binary.LittleEndian, hello.CipherSuites)
	return serverName + "+" + hex.EncodeToString(h.Sum(nil))[:8]
}

// certCache caches TLS certificates on disk to reduce TLS handshake latency.
// Before fetching a cert from exed, it checks the disk cache.
// If a cached cert exists and has not expired, it is returned directly
// and a background call to exed refreshes the cache without blocking
// the handshake. Otherwise, the fetch blocks on a call to exed and
// the result is stored.
//
// TODO: skip the background ping when the cert is far from expiry
// (e.g. time.Until(cert.Leaf.NotAfter) > 24h) to reduce exed load.
type certCache struct {
	dir string
	lg  *slog.Logger

	inflight singleflight.Group[string, *tls.Certificate]
}

func newCertCache(dir string, lg *slog.Logger) (*certCache, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("certcache: create dir: %w", err)
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("certcache: resolve dir: %w", err)
	}
	lg.Info("certcache: initialized", "dir", absDir)
	return &certCache{
		dir: absDir,
		lg:  lg,
	}, nil
}

// get returns a cert for the given cacheKey, using the disk cache when possible.
// If cc is nil, fetch is called directly.
// Use certCacheKey to compute an appropriate cacheKey.
func (cc *certCache) get(ctx context.Context, cacheKey string, fetch func(context.Context) (*tls.Certificate, error)) (*tls.Certificate, error) {
	if cc == nil {
		return fetch(ctx)
	}

	if strings.ContainsAny(cacheKey, "/\\\x00") {
		return nil, fmt.Errorf("certcache: invalid cache key %q", cacheKey)
	}

	cert, err := cc.loadFromDisk(cacheKey)
	// Serve from cache if the cert won't expire within 5 minutes.
	// The margin ensures callers never receive a cert that expires
	// mid-connection or during the next TLS handshake.
	if err == nil && cert.Leaf != nil && time.Until(cert.Leaf.NotAfter) > 5*time.Minute {
		// Call through to exed in the background so it sees active
		// domains and can manage cert renewal on its own schedule.
		// Singleflight deduplicates concurrent pings for the same key.
		// The 5-minute timeout bounds the goroutine if exed is unreachable,
		// since the gRPC client uses WaitForReady(true) with no per-call
		// deadline and would otherwise block indefinitely.
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			cc.fetchAndStore(ctx, cacheKey, fetch)
		}()
		return cert, nil
	}

	cert2, err2 := cc.fetchAndStore(ctx, cacheKey, fetch)
	if err2 != nil {
		return nil, err2
	}
	return cert2, nil
}

func (cc *certCache) loadFromDisk(cacheKey string) (*tls.Certificate, error) {
	data, err := os.ReadFile(cc.certPath(cacheKey))
	if err != nil {
		return nil, err
	}
	cert, err := wildcardcert.DecodeCertificate(data)
	if err != nil {
		return nil, err
	}
	if cert.Leaf == nil {
		cc.lg.Warn("certcache: removing cached cert with unparseable leaf", "cacheKey", cacheKey)
		os.Remove(cc.certPath(cacheKey))
		return nil, fmt.Errorf("certcache: unparseable leaf for %s", cacheKey)
	}
	return cert, nil
}

// storeToDisk writes a cert to disk atomically (temp file + rename).
func (cc *certCache) storeToDisk(cacheKey string, cert *tls.Certificate) error {
	data, err := wildcardcert.EncodeCertificate(cert)
	if err != nil {
		return err
	}

	tmp, err := os.CreateTemp(cc.dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Chmod(tmpName, 0600); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, cc.certPath(cacheKey)); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}

// fetchAndStore fetches a cert from exed, stores it to disk, and returns it.
// Concurrent calls for the same cacheKey are deduplicated via singleflight.
func (cc *certCache) fetchAndStore(ctx context.Context, cacheKey string, fetch func(context.Context) (*tls.Certificate, error)) (*tls.Certificate, error) {
	ch := cc.inflight.DoChan(cacheKey, func() (*tls.Certificate, error) {
		// Detach from the first caller's context so that if it is
		// cancelled (e.g. client disconnect), the shared fetch still
		// completes for all other waiters.
		fetchCtx, fetchCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Minute)
		defer fetchCancel()
		cert, err := fetch(fetchCtx)
		if err != nil {
			return nil, err
		}
		if storeErr := cc.storeToDisk(cacheKey, cert); storeErr != nil {
			cc.lg.ErrorContext(ctx, "certcache: failed to store cert", "cacheKey", cacheKey, "error", storeErr)
		}
		return cert, nil
	})
	select {
	case res := <-ch:
		return res.Val, res.Err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (cc *certCache) certPath(cacheKey string) string {
	return filepath.Join(cc.dir, cacheKey+".pem")
}
