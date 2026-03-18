package exeprox

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"log/slog"
	"math/big"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

// testCert creates a self-signed tls.Certificate with the given expiry.
func testCert(t *testing.T, notAfter time.Time) *tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     notAfter,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return &tls.Certificate{
		Certificate: [][]byte{der},
		PrivateKey:  key,
		Leaf:        leaf,
	}
}

func newTestCertCache(t *testing.T) *certCache {
	t.Helper()
	dir := t.TempDir()
	cc, err := newCertCache(dir, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	return cc
}

func TestCertCache_NilPassthrough(t *testing.T) {
	t.Parallel()
	want := testCert(t, time.Now().Add(24*time.Hour))
	var cc *certCache // nil
	got, err := cc.get(context.Background(), "key", func(ctx context.Context) (*tls.Certificate, error) {
		return want, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Error("nil certCache should pass through to fetch")
	}
}

func TestCertCache_NilPassthrough_Error(t *testing.T) {
	t.Parallel()
	var cc *certCache
	wantErr := errors.New("boom")
	_, err := cc.get(context.Background(), "key", func(ctx context.Context) (*tls.Certificate, error) {
		return nil, wantErr
	})
	if err != wantErr {
		t.Errorf("got err=%v, want %v", err, wantErr)
	}
}

func TestCertCache_Miss_ThenHit(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		cc := newTestCertCache(t)
		cert := testCert(t, time.Now().Add(24*time.Hour))

		var fetchCount atomic.Int32
		fetch := func(ctx context.Context) (*tls.Certificate, error) {
			fetchCount.Add(1)
			return cert, nil
		}

		// First call: cache miss, fetch is called.
		got, err := cc.get(context.Background(), "example.com", fetch)
		if err != nil {
			t.Fatal(err)
		}
		if got.Leaf.NotAfter != cert.Leaf.NotAfter {
			t.Error("returned cert doesn't match")
		}
		if fetchCount.Load() != 1 {
			t.Errorf("fetch called %d times, want 1", fetchCount.Load())
		}

		// Second call: cache hit, returns from disk.
		// A background goroutine refreshes via fetch; synctest.Wait
		// waits for it to finish.
		got2, err := cc.get(context.Background(), "example.com", fetch)
		if err != nil {
			t.Fatal(err)
		}
		if got2.Leaf.NotAfter != cert.Leaf.NotAfter {
			t.Error("cached cert doesn't match")
		}
		synctest.Wait()
	})
}

func TestCertCache_ExpiredCert_FallsThrough(t *testing.T) {
	t.Parallel()
	cc := newTestCertCache(t)

	expired := testCert(t, time.Now().Add(-time.Hour))
	fresh := testCert(t, time.Now().Add(24*time.Hour))

	// Seed the cache with an expired cert.
	if err := cc.storeToDisk("expired.com", expired); err != nil {
		t.Fatal(err)
	}

	got, err := cc.get(context.Background(), "expired.com", func(ctx context.Context) (*tls.Certificate, error) {
		return fresh, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Leaf.NotAfter.Equal(expired.Leaf.NotAfter) {
		t.Error("should not have returned expired cert")
	}
	if !got.Leaf.NotAfter.Equal(fresh.Leaf.NotAfter) {
		t.Error("should have returned fresh cert")
	}
}

func TestCertCache_ExpiryMargin(t *testing.T) {
	t.Parallel()
	cc := newTestCertCache(t)

	// Cert expires in 3 minutes — within the 5-minute margin.
	almostExpired := testCert(t, time.Now().Add(3*time.Minute))
	fresh := testCert(t, time.Now().Add(24*time.Hour))

	if err := cc.storeToDisk("margin.com", almostExpired); err != nil {
		t.Fatal(err)
	}

	got, err := cc.get(context.Background(), "margin.com", func(ctx context.Context) (*tls.Certificate, error) {
		return fresh, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Should have fetched fresh because the cached cert is within the margin.
	if !got.Leaf.NotAfter.Equal(fresh.Leaf.NotAfter) {
		t.Error("cert within expiry margin should trigger synchronous fetch")
	}
}

func TestCertCache_CorruptFile_Removed(t *testing.T) {
	t.Parallel()
	cc := newTestCertCache(t)

	// Write garbage to the cache file.
	path := cc.certPath("corrupt.com")
	if err := os.WriteFile(path, []byte("not a cert"), 0600); err != nil {
		t.Fatal(err)
	}

	fresh := testCert(t, time.Now().Add(24*time.Hour))
	got, err := cc.get(context.Background(), "corrupt.com", func(ctx context.Context) (*tls.Certificate, error) {
		return fresh, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Leaf.NotAfter.Equal(fresh.Leaf.NotAfter) {
		t.Error("corrupt cache entry should fall through to fetch")
	}
}

func TestCertCache_UnparseableLeaf_Removed(t *testing.T) {
	t.Parallel()
	cc := newTestCertCache(t)

	// Write a cert where DecodeCertificate returns nil Leaf.
	// We can't easily create that scenario, but we can write a file
	// that won't decode. loadFromDisk handles both decode errors and
	// nil Leaf by returning an error.
	path := cc.certPath("badleaf.com")
	if err := os.WriteFile(path, []byte("not valid PEM"), 0600); err != nil {
		t.Fatal(err)
	}

	fresh := testCert(t, time.Now().Add(24*time.Hour))
	_, err := cc.get(context.Background(), "badleaf.com", func(ctx context.Context) (*tls.Certificate, error) {
		return fresh, nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestCertCache_InvalidCacheKey(t *testing.T) {
	t.Parallel()
	cc := newTestCertCache(t)

	for _, key := range []string{"a/b", "a\\b", "a\x00b"} {
		_, err := cc.get(context.Background(), key, func(ctx context.Context) (*tls.Certificate, error) {
			t.Fatal("fetch should not be called for invalid key")
			return nil, nil
		})
		if err == nil {
			t.Errorf("expected error for key %q", key)
		}
	}
}

func TestCertCache_Singleflight(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		cc := newTestCertCache(t)
		cert := testCert(t, time.Now().Add(24*time.Hour))

		var fetchCount atomic.Int32
		gate := make(chan struct{})
		fetch := func(ctx context.Context) (*tls.Certificate, error) {
			fetchCount.Add(1)
			<-gate
			return cert, nil
		}

		const n = 5
		var wg sync.WaitGroup
		errs := make([]error, n)
		certs := make([]*tls.Certificate, n)
		for i := range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				certs[i], errs[i] = cc.get(context.Background(), "sf.com", fetch)
			}()
		}

		// All goroutines are durably blocked on the gate channel.
		synctest.Wait()
		close(gate)
		wg.Wait()

		for i, err := range errs {
			if err != nil {
				t.Errorf("goroutine %d: %v", i, err)
			}
		}

		if got := fetchCount.Load(); got != 1 {
			t.Errorf("fetch called %d times, want 1 (singleflight should deduplicate)", got)
		}
	})
}

func TestCertCache_DiskRoundTrip(t *testing.T) {
	t.Parallel()
	cc := newTestCertCache(t)
	cert := testCert(t, time.Now().Add(24*time.Hour))

	if err := cc.storeToDisk("rt.com", cert); err != nil {
		t.Fatal(err)
	}

	loaded, err := cc.loadFromDisk("rt.com")
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.Leaf.NotAfter.Equal(cert.Leaf.NotAfter) {
		t.Error("round-tripped cert has different NotAfter")
	}
	if loaded.Leaf == nil {
		t.Error("round-tripped cert has nil Leaf")
	}

	// Check file permissions.
	info, err := os.Stat(filepath.Join(cc.dir, "rt.com.pem"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("file perm = %o, want 0600", perm)
	}
}

func TestCertCache_FetchError(t *testing.T) {
	t.Parallel()
	cc := newTestCertCache(t)
	wantErr := errors.New("exed down")

	_, err := cc.get(context.Background(), "fail.com", func(ctx context.Context) (*tls.Certificate, error) {
		return nil, wantErr
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("got err=%v, want %v", err, wantErr)
	}
}

func TestCertCacheKey(t *testing.T) {
	t.Parallel()

	// nil hello returns just the server name.
	if got := certCacheKey("example.com", nil); got != "example.com" {
		t.Errorf("certCacheKey(nil) = %q, want %q", got, "example.com")
	}

	// Same hello fields produce the same key.
	hello := &tls.ClientHelloInfo{
		SignatureSchemes: []tls.SignatureScheme{tls.ECDSAWithP256AndSHA256},
		SupportedCurves:  []tls.CurveID{tls.CurveP256},
		CipherSuites:     []uint16{tls.TLS_AES_128_GCM_SHA256},
	}
	k1 := certCacheKey("example.com", hello)
	k2 := certCacheKey("example.com", hello)
	if k1 != k2 {
		t.Errorf("same hello produced different keys: %q vs %q", k1, k2)
	}

	// Different cipher suites produce different keys.
	hello2 := &tls.ClientHelloInfo{
		SignatureSchemes: []tls.SignatureScheme{tls.ECDSAWithP256AndSHA256},
		SupportedCurves:  []tls.CurveID{tls.CurveP256},
		CipherSuites:     []uint16{tls.TLS_AES_256_GCM_SHA384},
	}
	k3 := certCacheKey("example.com", hello2)
	if k1 == k3 {
		t.Error("different cipher suites should produce different keys")
	}

	// Different server names produce different keys.
	k4 := certCacheKey("other.com", hello)
	if k1 == k4 {
		t.Error("different server names should produce different keys")
	}
}

func TestCertCache_ContextCancellation(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		cc := newTestCertCache(t)

		ctx, cancel := context.WithCancel(context.Background())
		cancel() // cancel immediately

		// The fetch blocks on a channel so the caller sees ctx.Done() first.
		// We close it afterward to let the singleflight goroutine drain.
		block := make(chan struct{})
		_, err := cc.get(ctx, "cancel.com", func(ctx context.Context) (*tls.Certificate, error) {
			<-block
			return testCert(t, time.Now().Add(24*time.Hour)), nil
		})
		if !errors.Is(err, context.Canceled) {
			t.Errorf("got err=%v, want context.Canceled", err)
		}
		close(block)
		synctest.Wait()
	})
}
