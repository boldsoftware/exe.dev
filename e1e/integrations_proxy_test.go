package e1e

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// httpbinTarget is the top-level httpbin.org URL used as integration target.
const httpbinTarget = "https://httpbin.org"

// TestIntegrationsProxy tests the full integration proxy flow:
// VM → exelet metadata (169.254.169.254) → target (httpbin.org).
func TestIntegrationsProxy(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 1)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	// Register a user and create a VM.
	pty, _, keyFile, _ := registerForExeDev(t)
	bn := boxName(t)
	pty.SendLine(fmt.Sprintf("new --name=%s", bn))
	pty.WantRE("Creating .*" + bn)
	pty.Want("Ready")
	pty.WantPrompt()

	// Wait for SSH to be ready.
	waitForSSH(t, bn, keyFile)

	// Add an integration pointing to httpbin.org.
	pty.SendLine(fmt.Sprintf("integrations add http-proxy --name=echoproxy --target=%s --header=X-Custom-Auth:test-secret-123", httpbinTarget))
	pty.Want("Added integration echoproxy")
	pty.WantPrompt()

	// Attach the integration to the VM.
	pty.SendLine(fmt.Sprintf("integrations attach echoproxy %s", bn))
	pty.Want("Attached echoproxy to vm:" + bn)
	pty.WantPrompt()

	// Helper to curl from inside the VM with retry.
	curlRetry := func(t *testing.T, curlArgs, want string) string {
		t.Helper()
		cmd := fmt.Sprintf(`curl --max-time 10 -s %s`, curlArgs)
		var response string
		deadline := time.Now().Add(30 * time.Second)
		for {
			out, _ := boxSSHShell(t, bn, keyFile, cmd).CombinedOutput()
			response = string(out)
			if strings.Contains(response, want) {
				return response
			}
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %q in response:\n%s", want, response)
			}
			time.Sleep(200 * time.Millisecond)
		}
	}

	parseHTTPBin := func(t *testing.T, raw string) map[string]any {
		t.Helper()
		var result map[string]any
		if err := json.Unmarshal([]byte(raw), &result); err != nil {
			t.Fatalf("failed to parse httpbin response: %v\nraw: %s", err, raw)
		}
		return result
	}

	t.Run("basic_proxy", func(t *testing.T) {
		response := curlRetry(t, "http://echoproxy.int.exe.cloud/anything", "X-Custom-Auth")
		result := parseHTTPBin(t, response)
		headers, _ := result["headers"].(map[string]any)
		if got := headers["X-Custom-Auth"]; got != "test-secret-123" {
			t.Errorf("expected X-Custom-Auth=test-secret-123, got %v", got)
		}
	})

	t.Run("path_passthrough", func(t *testing.T) {
		response := curlRetry(t, "http://echoproxy.int.exe.cloud/anything/some/deep/path", "some/deep/path")
		result := parseHTTPBin(t, response)
		if u, _ := result["url"].(string); !strings.Contains(u, "/anything/some/deep/path") {
			t.Errorf("expected url containing /anything/some/deep/path, got %s", u)
		}
	})

	t.Run("query_passthrough", func(t *testing.T) {
		response := curlRetry(t, "'http://echoproxy.int.exe.cloud/anything?bar=baz&q=1'", "bar")
		result := parseHTTPBin(t, response)
		args, _ := result["args"].(map[string]any)
		if args["bar"] != "baz" {
			t.Errorf("expected args.bar=baz, got %v", args["bar"])
		}
	})

	t.Run("post_method", func(t *testing.T) {
		response := curlRetry(t, "-X POST http://echoproxy.int.exe.cloud/anything", "POST")
		result := parseHTTPBin(t, response)
		if method, _ := result["method"].(string); method != "POST" {
			t.Errorf("expected POST method, got %s", method)
		}
	})

	t.Run("detached_forbidden", func(t *testing.T) {
		pty.SendLine(fmt.Sprintf("integrations detach echoproxy %s", bn))
		pty.Want("Detached echoproxy from vm:" + bn)
		pty.WantPrompt()

		curlRetry(t, "-o /dev/null -w '%{http_code}' http://echoproxy.int.exe.cloud/", "403")

		pty.SendLine(fmt.Sprintf("integrations attach echoproxy %s", bn))
		pty.Want("Attached echoproxy to vm:" + bn)
		pty.WantPrompt()
	})

	t.Run("nonexistent_integration", func(t *testing.T) {
		curlRetry(t, "-o /dev/null -w '%{http_code}' http://doesnotexist.int.exe.cloud/", "403")
	})

	t.Run("removed_integration", func(t *testing.T) {
		pty.SendLine("integrations remove echoproxy")
		pty.Want("Removed integration echoproxy")
		pty.WantPrompt()

		curlRetry(t, "-o /dev/null -w '%{http_code}' http://echoproxy.int.exe.cloud/", "403")
	})

	t.Run("url_credentials", func(t *testing.T) {
		pty.SendLine("integrations add http-proxy --name=authproxy --target=https://testuser:testpass@httpbin.org --header=X-Custom:val")
		pty.Want("Added integration authproxy")
		pty.WantPrompt()
		pty.SendLine(fmt.Sprintf("integrations attach authproxy %s", bn))
		pty.Want("Attached authproxy to vm:" + bn)
		pty.WantPrompt()

		response := curlRetry(t, "http://authproxy.int.exe.cloud/anything", "Authorization")
		result := parseHTTPBin(t, response)
		headers, _ := result["headers"].(map[string]any)
		auth, _ := headers["Authorization"].(string)
		if !strings.Contains(auth, "Basic") {
			t.Errorf("expected Basic auth header, got %q", auth)
		}

		pty.SendLine("integrations remove authproxy")
		pty.Want("Removed")
		pty.WantPrompt()
	})

	t.Run("unresolvable_target", func(t *testing.T) {
		pty.SendLine("integrations add http-proxy --name=badtarget --target=https://this-domain-does-not-exist-abc123.example.com --header=X-Auth:secret")
		pty.Want("Added integration badtarget")
		pty.WantPrompt()
		pty.SendLine(fmt.Sprintf("integrations attach badtarget %s", bn))
		pty.Want("Attached badtarget to vm:" + bn)
		pty.WantPrompt()

		response := curlRetry(t, "-w '\n%{http_code}' http://badtarget.int.exe.cloud/", "502")
		if strings.Contains(response, "this-domain-does-not-exist") {
			t.Errorf("error response leaks target hostname: %s", response)
		}
		if !strings.Contains(response, "upstream request failed") {
			t.Errorf("expected generic 'upstream request failed' error, got: %s", response)
		}

		pty.SendLine("integrations remove badtarget")
		pty.Want("Removed")
		pty.WantPrompt()
	})

	t.Run("invalid_proxy_name", func(t *testing.T) {
		curlRetry(t, "-o /dev/null -w '%{http_code}' http://INVALID.int.exe.cloud/", "400")
	})

	// Security: creation-time validation tests

	t.Run("reject_http_scheme", func(t *testing.T) {
		pty.SendLine("integrations add http-proxy --name=httponly --target=http://example.com --header=X-Auth:secret")
		pty.Want("scheme must be https")
		pty.WantPrompt()
	})

	t.Run("reject_non443_port", func(t *testing.T) {
		pty.SendLine("integrations add http-proxy --name=badport --target=https://example.com:8080 --header=X-Auth:secret")
		pty.Want("port 443")
		pty.WantPrompt()
	})

	t.Run("reject_bare_ip", func(t *testing.T) {
		pty.SendLine("integrations add http-proxy --name=bareip --target=https://10.0.0.1 --header=X-Auth:secret")
		pty.Want("hostname, not an IP")
		pty.WantPrompt()
	})

	t.Run("reject_tsnet_domain", func(t *testing.T) {
		pty.SendLine("integrations add http-proxy --name=tsnet --target=https://myhost.ts.net --header=X-Auth:secret")
		pty.Want(".ts.net")
		pty.WantPrompt()
	})

	t.Run("reject_exe_cloud_domain", func(t *testing.T) {
		pty.SendLine("integrations add http-proxy --name=execloud --target=https://test.exe.cloud --header=X-Auth:secret")
		pty.Want(".exe.cloud")
		pty.WantPrompt()
	})

	t.Run("reject_exe_dev_domain", func(t *testing.T) {
		pty.SendLine("integrations add http-proxy --name=exedev --target=https://test.exe.dev --header=X-Auth:secret")
		pty.Want(".exe.dev")
		pty.WantPrompt()
	})

	t.Run("reject_localhost", func(t *testing.T) {
		pty.SendLine("integrations add http-proxy --name=localh --target=https://localhost --header=X-Auth:secret")
		pty.Want("localhost")
		pty.WantPrompt()
	})

	t.Run("reject_reserved_header", func(t *testing.T) {
		pty.SendLine("integrations add http-proxy --name=badheader --target=https://example.com --header=X-Exedev-Box:evil")
		pty.Want("reserved")
		pty.WantPrompt()
	})

	t.Run("reject_invalid_name_creation", func(t *testing.T) {
		pty.SendLine("integrations add http-proxy --name=-bad --target=https://example.com --header=X-Auth:secret")
		pty.Want("invalid name")
		pty.WantPrompt()
	})

	t.Run("bearer_flag_proxy", func(t *testing.T) {
		pty.SendLine(fmt.Sprintf("integrations add http-proxy --name=bearertest --target=%s --bearer=proxy-test-token-789", httpbinTarget))
		pty.Want("Added integration bearertest")
		pty.WantPrompt()
		pty.SendLine(fmt.Sprintf("integrations attach bearertest %s", bn))
		pty.Want("Attached bearertest to vm:" + bn)
		pty.WantPrompt()

		response := curlRetry(t, "http://bearertest.int.exe.cloud/anything", "Authorization")
		result := parseHTTPBin(t, response)
		headers, _ := result["headers"].(map[string]any)
		auth, _ := headers["Authorization"].(string)
		if auth != "Bearer proxy-test-token-789" {
			t.Errorf("expected Authorization header 'Bearer proxy-test-token-789', got %q", auth)
		}

		pty.SendLine("integrations remove bearertest")
		pty.Want("Removed")
		pty.WantPrompt()
	})

	cleanupBox(t, keyFile, bn)
}
