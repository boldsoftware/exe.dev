package execore

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBuildHTTPProxyConfig(t *testing.T) {
	t.Parallel()
	t.Run("basic", func(t *testing.T) {
		cfg := `{"target":"https://httpbin.org/anything","header":"X-Custom:secret"}`
		resp, err := buildHTTPProxyConfig(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if !resp.OK {
			t.Fatal("expected OK")
		}
		if resp.Target != "https://httpbin.org/anything" {
			t.Errorf("target = %q, want https://httpbin.org/anything", resp.Target)
		}
		if resp.Headers["X-Custom"] != "secret" {
			t.Errorf("header X-Custom = %q, want secret", resp.Headers["X-Custom"])
		}
		if resp.BasicAuth != nil {
			t.Error("expected no basic auth")
		}
	})

	t.Run("url_credentials", func(t *testing.T) {
		cfg := `{"target":"https://testuser:testpass@httpbin.org","header":"X-Custom:val"}`
		resp, err := buildHTTPProxyConfig(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if !resp.OK {
			t.Fatal("expected OK")
		}
		if resp.Target != "https://httpbin.org" {
			t.Errorf("target = %q, want https://httpbin.org (credentials stripped)", resp.Target)
		}
		if resp.BasicAuth == nil {
			t.Fatal("expected basic auth from URL credentials")
		}
		if resp.BasicAuth.User != "testuser" {
			t.Errorf("basic auth user = %q, want testuser", resp.BasicAuth.User)
		}
		if resp.BasicAuth.Pass != "testpass" {
			t.Errorf("basic auth pass = %q, want testpass", resp.BasicAuth.Pass)
		}
	})
}

func TestHandlePeerProxy_RequiresTailscaleIP(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	s.env.GatewayDev = false // Production mode — requires Tailscale IP.

	// Default httptest RemoteAddr is 192.0.2.1:1234, not a Tailscale IP.
	req := httptest.NewRequest("POST", "/_/peer-proxy", nil)
	w := httptest.NewRecorder()

	s.requireTailscaleOrDev(s.handlePeerProxy).ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for non-Tailscale IP, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandlePeerProxy_TailscaleIPAllowed(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	s.env.GatewayDev = false // Production mode — requires Tailscale IP.

	// Tailscale IP should pass auth and reach the next validation (missing headers → 400).
	req := httptest.NewRequest("POST", "/_/peer-proxy", nil)
	req.RemoteAddr = "100.64.0.1:1234"
	w := httptest.NewRecorder()

	s.requireTailscaleOrDev(s.handlePeerProxy).ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 (missing headers) for Tailscale IP, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandlePeerProxy_GatewayDevBypass(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	s.env.GatewayDev = true

	// With GatewayDev the Tailscale check is skipped; we should reach
	// the next validation (missing headers → 400), not 401.
	req := httptest.NewRequest("POST", "/_/peer-proxy", nil)
	w := httptest.NewRecorder()

	s.requireTailscaleOrDev(s.handlePeerProxy).ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 (missing headers) with GatewayDev, got %d: %s", w.Code, w.Body.String())
	}
}
