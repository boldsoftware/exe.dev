package execore

import (
	"testing"
)

func TestBuildHTTPProxyConfig(t *testing.T) {
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
