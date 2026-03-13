package e1e

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"exe.dev/e1e/testinfra"
	"exe.dev/stage"
	"golang.org/x/crypto/ssh"
)

// tradeExe0ForExe1 generates an exe0 token, runs the exe0-to-exe1 SSH command,
// and returns the resulting exe1 token.
func tradeExe0ForExe1(t *testing.T, keyFile, exe0Token string, extraArgs ...string) (string, error) {
	t.Helper()
	args := append([]string{"exe0-to-exe1"}, extraArgs...)
	args = append(args, exe0Token)
	out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), keyFile, args...)
	if err != nil {
		return "", fmt.Errorf("exe0-to-exe1 failed: %v\n%s", err, out)
	}
	token := strings.TrimSpace(string(out))
	return token, nil
}

// tradeExe0ForExe1JSON generates an exe0 token, runs the exe0-to-exe1 --json SSH command,
// and returns the raw JSON output.
func tradeExe0ForExe1JSON(t *testing.T, keyFile, exe0Token string, extraArgs ...string) ([]byte, error) {
	t.Helper()
	args := append([]string{"exe0-to-exe1", "--json"}, extraArgs...)
	args = append(args, exe0Token)
	out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), keyFile, args...)
	if err != nil {
		return out, fmt.Errorf("exe0-to-exe1 --json failed: %v\n%s", err, out)
	}
	return out, nil
}

// TestExe1TokenTrade tests the basic exe0-to-exe1 token trade via SSH.
func TestExe1TokenTrade(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, _, keyFile, _ := registerForExeDev(t)
	pty.Disconnect()

	signer := loadTestSigner(t, keyFile)
	exe0Token := generateToken(t, signer, `{}`, "v0@"+stage.Test().WebHost)

	exe1Token, err := tradeExe0ForExe1(t, keyFile, exe0Token)
	if err != nil {
		t.Fatalf("trade failed: %v", err)
	}

	if !strings.HasPrefix(exe1Token, "exe1.") {
		t.Fatalf("expected exe1 token to start with 'exe1.', got %q", exe1Token)
	}
}

// TestExe1TokenTradeJSON tests exe0-to-exe1 with --json flag.
func TestExe1TokenTradeJSON(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, _, keyFile, _ := registerForExeDev(t)
	pty.Disconnect()

	signer := loadTestSigner(t, keyFile)
	exe0Token := generateToken(t, signer, `{}`, "v0@"+stage.Test().WebHost)

	out, err := tradeExe0ForExe1JSON(t, keyFile, exe0Token)
	if err != nil {
		t.Fatalf("trade failed: %v", err)
	}

	var result map[string]string
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("expected JSON output, got: %s", out)
	}

	token, ok := result["token"]
	if !ok {
		t.Fatalf("expected 'token' key in JSON output, got: %s", out)
	}
	if !strings.HasPrefix(token, "exe1.") {
		t.Fatalf("expected token to start with 'exe1.', got %q", token)
	}
}

// TestExe1TokenTradeWithVM tests exe0-to-exe1 with --vm flag for a VM the user owns.
func TestExe1TokenTradeWithVM(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 1)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, _, keyFile, _ := registerForExeDev(t)
	box := newBox(t, pty, testinfra.BoxOpts{Command: "/bin/bash"})
	pty.Disconnect()

	signer := loadTestSigner(t, keyFile)
	exe0Token := generateToken(t, signer, `{}`, "v0@"+box+"."+stage.Test().BoxHost)

	exe1Token, err := tradeExe0ForExe1(t, keyFile, exe0Token, "--vm="+box)
	if err != nil {
		t.Fatalf("trade failed: %v", err)
	}

	if !strings.HasPrefix(exe1Token, "exe1.") {
		t.Fatalf("expected exe1 token to start with 'exe1.', got %q", exe1Token)
	}

	cleanupBox(t, keyFile, box)
}

// TestExe1TokenTradeWithVMNotOwned tests that --vm fails when the user doesn't own the VM.
func TestExe1TokenTradeWithVMNotOwned(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, _, keyFile, _ := registerForExeDev(t)
	pty.Disconnect()

	signer := loadTestSigner(t, keyFile)
	exe0Token := generateToken(t, signer, `{}`, "v0@nonexistent-vm."+stage.Test().BoxHost)

	_, err := tradeExe0ForExe1(t, keyFile, exe0Token, "--vm=nonexistent-vm")
	if err == nil {
		t.Fatal("expected trade to fail for VM not owned by user, but it succeeded")
	}
	if !strings.Contains(err.Error(), "not found or access denied") {
		t.Fatalf("expected 'not found or access denied' error, got: %v", err)
	}
}

// TestExe1TokenTradeVMNsMismatch tests that --vm rejects a token
// signed for a different VM namespace (e.g., token scoped to boxA but traded
// with --vm=boxB).
func TestExe1TokenTradeVMNsMismatch(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 1)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, _, keyFile, _ := registerForExeDev(t)
	box := newBox(t, pty, testinfra.BoxOpts{Command: "/bin/bash"})
	pty.Disconnect()

	// Sign the token for a different namespace than the box we'll pass to --vm.
	signer := loadTestSigner(t, keyFile)
	exe0Token := generateToken(t, signer, `{}`, "v0@wrong-namespace."+stage.Test().BoxHost)

	_, err := tradeExe0ForExe1(t, keyFile, exe0Token, "--vm="+box)
	if err == nil {
		t.Fatal("expected trade to fail for namespace-mismatched token, but it succeeded")
	}
	if !strings.Contains(err.Error(), "invalid token") {
		t.Fatalf("expected 'invalid token' error, got: %v", err)
	}

	cleanupBox(t, keyFile, box)
}

// TestExe1TokenTradeRejectsExpired tests that expired exe0 tokens are rejected at trade time.
func TestExe1TokenTradeRejectsExpired(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, _, keyFile, _ := registerForExeDev(t)
	pty.Disconnect()

	signer := loadTestSigner(t, keyFile)
	// exp=1000000000 is Sep 2001, well in the past.
	exe0Token := generateToken(t, signer, `{"exp":1000000000}`, "v0@"+stage.Test().WebHost)

	_, err := tradeExe0ForExe1(t, keyFile, exe0Token)
	if err == nil {
		t.Fatal("expected trade to fail for expired token, but it succeeded")
	}
	if !strings.Contains(err.Error(), "token has expired") {
		t.Fatalf("expected 'token has expired' error, got: %v", err)
	}
}

// TestExe1TokenTradeAllowsNotYetValid tests that exe0 tokens with future nbf are accepted.
func TestExe1TokenTradeAllowsNotYetValid(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, _, keyFile, _ := registerForExeDev(t)
	pty.Disconnect()

	signer := loadTestSigner(t, keyFile)
	// nbf=4000000000 is Oct 2096, in the future.
	exe0Token := generateToken(t, signer, `{"nbf":4000000000}`, "v0@"+stage.Test().WebHost)

	exe1Token, err := tradeExe0ForExe1(t, keyFile, exe0Token)
	if err != nil {
		t.Fatalf("trade should succeed for future nbf, got: %v", err)
	}

	if !strings.HasPrefix(exe1Token, "exe1.") {
		t.Fatalf("expected exe1 token to start with 'exe1.', got %q", exe1Token)
	}
}

// TestExe1TokenTradeRejectsInvalid tests that various invalid inputs are rejected.
func TestExe1TokenTradeRejectsInvalid(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, _, keyFile, _ := registerForExeDev(t)
	pty.Disconnect()

	signer := loadTestSigner(t, keyFile)

	t.Run("garbage_input", func(t *testing.T) {
		_, err := tradeExe0ForExe1(t, keyFile, "totalgarbage")
		if err == nil {
			t.Fatal("expected trade to fail for garbage input")
		}
		if !strings.Contains(err.Error(), "invalid token") {
			t.Fatalf("expected 'invalid token' error, got: %v", err)
		}
	})

	t.Run("bad_format", func(t *testing.T) {
		_, err := tradeExe0ForExe1(t, keyFile, "exe0.onlyonedot")
		if err == nil {
			t.Fatal("expected trade to fail for bad format")
		}
		if !strings.Contains(err.Error(), "invalid token") {
			t.Fatalf("expected 'invalid token' error, got: %v", err)
		}
	})

	t.Run("wrong_namespace", func(t *testing.T) {
		// Sign with the wrong namespace.
		wrongToken := generateToken(t, signer, `{}`, "wrong@namespace")
		_, err := tradeExe0ForExe1(t, keyFile, wrongToken)
		if err == nil {
			t.Fatal("expected trade to fail for wrong namespace token")
		}
		if !strings.Contains(err.Error(), "invalid token") {
			t.Fatalf("expected 'invalid token' error, got: %v", err)
		}
	})
}

// TestExe1TokenTradeIdempotent tests that trading the same exe0 token twice returns
// the same exe1 token.
func TestExe1TokenTradeIdempotent(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, _, keyFile, _ := registerForExeDev(t)
	pty.Disconnect()

	signer := loadTestSigner(t, keyFile)
	exe0Token := generateToken(t, signer, `{}`, "v0@"+stage.Test().WebHost)

	exe1First, err := tradeExe0ForExe1(t, keyFile, exe0Token)
	if err != nil {
		t.Fatalf("first trade failed: %v", err)
	}

	exe1Second, err := tradeExe0ForExe1(t, keyFile, exe0Token)
	if err != nil {
		t.Fatalf("second trade failed: %v", err)
	}

	if exe1First != exe1Second {
		t.Fatalf("expected idempotent trade, got %q and %q", exe1First, exe1Second)
	}
}

// TestExe1TokenDeletedKeyAtUseTime tests that an exe1 token is rejected when
// the underlying SSH key has been deleted.
func TestExe1TokenDeletedKeyAtUseTime(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, _, keyFile, _ := registerForExeDev(t)
	pty.Disconnect()

	signer := loadTestSigner(t, keyFile)
	exe0Token := generateToken(t, signer, `{}`, "v0@"+stage.Test().WebHost)

	exe1Token, err := tradeExe0ForExe1(t, keyFile, exe0Token)
	if err != nil {
		t.Fatalf("trade failed: %v", err)
	}

	// Delete the SSH key that backs the exe0 token.
	fingerprint := ssh.FingerprintSHA256(signer.PublicKey())
	out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), keyFile, "ssh-key", "remove", fingerprint)
	if err != nil {
		t.Fatalf("ssh-key remove failed: %v\n%s", err, out)
	}

	// Use the exe1 token — the underlying SSH key is gone.
	baseURL := fmt.Sprintf("http://localhost:%d", Env.servers.Exed.HTTPPort)
	req, err := http.NewRequest("POST", baseURL+"/exec", strings.NewReader("whoami"))
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+exe1Token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 401 for deleted SSH key, got %d: %s", resp.StatusCode, body)
	}
}

// TestExe1TokenExecAPI tests that an exe1 token works as Bearer for the /exec endpoint.
func TestExe1TokenExecAPI(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, _, keyFile, email := registerForExeDev(t)
	pty.Disconnect()

	// Generate exe0 token scoped to webhost, trade for exe1.
	signer := loadTestSigner(t, keyFile)
	exe0Token := generateToken(t, signer, `{}`, "v0@"+stage.Test().WebHost)

	exe1Token, err := tradeExe0ForExe1(t, keyFile, exe0Token)
	if err != nil {
		t.Fatalf("trade failed: %v", err)
	}

	// Use exe1 token as Bearer for /exec whoami.
	baseURL := fmt.Sprintf("http://localhost:%d", Env.servers.Exed.HTTPPort)
	req, err := http.NewRequest("POST", baseURL+"/exec", strings.NewReader("whoami"))
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+exe1Token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}

	if !strings.Contains(string(body), email) {
		t.Errorf("expected output to contain email %q, got: %s", email, body)
	}
}

// TestExe1TokenCmdsRestricted tests that an exe1 token wrapping a cmds-restricted exe0
// token enforces the restriction at use-time via the exec API.
func TestExe1TokenCmdsRestricted(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, _, keyFile, _ := registerForExeDev(t)
	pty.Disconnect()

	signer := loadTestSigner(t, keyFile)
	// Token restricted to "whoami" only — "exe0-to-exe1" is not in the cmds list,
	// but the trade itself is an SSH command (key-authed) and not subject to cmds.
	exe0Token := generateToken(t, signer, `{"cmds":["whoami"]}`, "v0@"+stage.Test().WebHost)

	exe1Token, err := tradeExe0ForExe1(t, keyFile, exe0Token)
	if err != nil {
		t.Fatalf("trade failed: %v", err)
	}

	baseURL := fmt.Sprintf("http://localhost:%d", Env.servers.Exed.HTTPPort)

	// whoami should succeed — it's in the cmds list.
	req, err := http.NewRequest("POST", baseURL+"/exec", strings.NewReader("whoami"))
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+exe1Token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200 for whoami, got %d: %s", resp.StatusCode, body)
	}

	// ls should be rejected — not in the cmds list.
	req2, err := http.NewRequest("POST", baseURL+"/exec", strings.NewReader("ls"))
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}
	req2.Header.Set("Authorization", "Bearer "+exe1Token)

	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp2.Body.Close()

	if resp2.StatusCode != http.StatusForbidden {
		body, _ := io.ReadAll(resp2.Body)
		t.Fatalf("expected 403 for ls with cmds-restricted token, got %d: %s", resp2.StatusCode, body)
	}
	body, _ := io.ReadAll(resp2.Body)
	if !strings.Contains(string(body), "command not allowed") {
		t.Fatalf("expected 'command not allowed' error, got: %s", body)
	}
}

// TestExe1TokenProxyBearer tests that an exe1 token works as Bearer for proxy HTTP requests.
func TestExe1TokenProxyBearer(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 1)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, _, keyFile, _ := registerForExeDev(t)
	box := newBox(t, pty, testinfra.BoxOpts{Command: "/bin/bash"})
	pty.Disconnect()
	waitForSSH(t, box, keyFile)

	// Start HTTP server on the box.
	serveIndex(t, box, keyFile, "proxy-bearer-test")

	// Configure as private route.
	configureProxyRoute(t, keyFile, box, 8080, "private")

	// Generate exe0 token scoped to the VM, trade for exe1.
	signer := loadTestSigner(t, keyFile)
	exe0Token := generateToken(t, signer, `{}`, "v0@"+box+"."+stage.Test().BoxHost)

	exe1Token, err := tradeExe0ForExe1(t, keyFile, exe0Token, "--vm="+box)
	if err != nil {
		t.Fatalf("trade failed: %v", err)
	}

	// Use exe1 token as Bearer for proxy request.
	httpPort := Env.servers.Exeprox.HTTPPort
	proxyURL := fmt.Sprintf("http://%s.exe.cloud:%d/", box, httpPort)
	client := noRedirectClient(nil)
	req, err := localhostRequestWithHostHeader("GET", proxyURL, nil)
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+exe1Token)

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}

	if !strings.Contains(string(body), "proxy-bearer-test") {
		t.Errorf("expected body to contain 'proxy-bearer-test', got: %s", body)
	}

	cleanupBox(t, keyFile, box)
}

// TestExe1TokenProxyBasic tests that an exe1 token works as Basic auth password for proxy.
func TestExe1TokenProxyBasic(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 1)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, _, keyFile, _ := registerForExeDev(t)
	box := newBox(t, pty, testinfra.BoxOpts{Command: "/bin/bash"})
	pty.Disconnect()
	waitForSSH(t, box, keyFile)

	// Start HTTP server on the box.
	serveIndex(t, box, keyFile, "proxy-basic-test")

	// Configure as private route.
	configureProxyRoute(t, keyFile, box, 8080, "private")

	// Generate exe0 token scoped to the VM, trade for exe1.
	signer := loadTestSigner(t, keyFile)
	exe0Token := generateToken(t, signer, `{}`, "v0@"+box+"."+stage.Test().BoxHost)

	exe1Token, err := tradeExe0ForExe1(t, keyFile, exe0Token, "--vm="+box)
	if err != nil {
		t.Fatalf("trade failed: %v", err)
	}

	// Use exe1 token as Basic auth password for proxy request.
	httpPort := Env.servers.Exeprox.HTTPPort
	proxyURL := fmt.Sprintf("http://%s.exe.cloud:%d/", box, httpPort)
	client := noRedirectClient(nil)
	req, err := localhostRequestWithHostHeader("GET", proxyURL, nil)
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}
	req.SetBasicAuth("anyuser", exe1Token)

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}

	if !strings.Contains(string(body), "proxy-basic-test") {
		t.Errorf("expected body to contain 'proxy-basic-test', got: %s", body)
	}

	cleanupBox(t, keyFile, box)
}

// TestExe1TokenInvalid tests that a non-existent exe1 token returns 401.
func TestExe1TokenInvalid(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 1)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, _, keyFile, _ := registerForExeDev(t)
	box := newBox(t, pty, testinfra.BoxOpts{Command: "/bin/bash"})
	pty.Disconnect()
	waitForSSH(t, box, keyFile)

	// Start HTTP server on the box.
	serveIndex(t, box, keyFile, "invalid-token-test")

	// Configure as private route.
	configureProxyRoute(t, keyFile, box, 8080, "private")

	fakeToken := "exe1.LYESXTHXY46PCIGQ3RUISD64W2"

	t.Run("format_invalid_exec_api", func(t *testing.T) {
		formatInvalid := "exe1.doesnotexist"
		baseURL := fmt.Sprintf("http://localhost:%d", Env.servers.Exed.HTTPPort)
		req, err := http.NewRequest("POST", baseURL+"/exec", strings.NewReader("whoami"))
		if err != nil {
			t.Fatalf("failed to create request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+formatInvalid)

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusUnauthorized {
			body, _ := io.ReadAll(resp.Body)
			t.Errorf("expected 401 for format-invalid exe1 token on /exec, got %d: %s", resp.StatusCode, body)
		}
	})

	t.Run("exec_api", func(t *testing.T) {
		baseURL := fmt.Sprintf("http://localhost:%d", Env.servers.Exed.HTTPPort)
		req, err := http.NewRequest("POST", baseURL+"/exec", strings.NewReader("whoami"))
		if err != nil {
			t.Fatalf("failed to create request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+fakeToken)

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusUnauthorized {
			body, _ := io.ReadAll(resp.Body)
			t.Errorf("expected 401 for fake exe1 token on /exec, got %d: %s", resp.StatusCode, body)
		}
	})

	t.Run("proxy_bearer", func(t *testing.T) {
		httpPort := Env.servers.Exeprox.HTTPPort
		proxyURL := fmt.Sprintf("http://%s.exe.cloud:%d/", box, httpPort)
		client := noRedirectClient(nil)
		req, err := localhostRequestWithHostHeader("GET", proxyURL, nil)
		if err != nil {
			t.Fatalf("failed to create request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+fakeToken)

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusUnauthorized {
			body, _ := io.ReadAll(resp.Body)
			t.Errorf("expected 401 for fake exe1 token on proxy, got %d: %s", resp.StatusCode, body)
		}
	})

	t.Run("proxy_basic", func(t *testing.T) {
		httpPort := Env.servers.Exeprox.HTTPPort
		proxyURL := fmt.Sprintf("http://%s.exe.cloud:%d/", box, httpPort)
		client := noRedirectClient(nil)
		req, err := localhostRequestWithHostHeader("GET", proxyURL, nil)
		if err != nil {
			t.Fatalf("failed to create request: %v", err)
		}
		req.SetBasicAuth("anyuser", fakeToken)

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusUnauthorized {
			body, _ := io.ReadAll(resp.Body)
			t.Errorf("expected 401 for fake exe1 token via basic auth on proxy, got %d: %s", resp.StatusCode, body)
		}
	})

	cleanupBox(t, keyFile, box)
}

// TestExe1TokenStripping verifies that exe1 token auth headers are stripped
// when proxying to the VM backend.
func TestExe1TokenStripping(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 1)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, _, keyFile, _ := registerForExeDev(t)
	box := newBox(t, pty, testinfra.BoxOpts{Command: "/bin/bash"})
	pty.Disconnect()
	waitForSSH(t, box, keyFile)

	// Start HTTP server on the box.
	startHTTPServer(t, box, keyFile, 8080)

	// Create a CGI script that echoes all env vars (to inspect forwarded headers).
	writeCGI := boxSSHCommand(t, box, keyFile, "sh", "-c", `set -e
mkdir -p /home/exedev/cgi-bin
cat <<'EOF' >/home/exedev/cgi-bin/headers
#!/bin/sh
echo "Content-Type: text/plain"
echo
env
EOF
chmod +x /home/exedev/cgi-bin/headers
`)
	if err := writeCGI.Run(); err != nil {
		t.Fatalf("failed to configure header CGI: %v", err)
	}

	// Configure as private route.
	configureProxyRoute(t, keyFile, box, 8080, "private")

	// Generate exe0 token scoped to the VM, trade for exe1.
	signer := loadTestSigner(t, keyFile)
	exe0Token := generateToken(t, signer, `{}`, "v0@"+box+"."+stage.Test().BoxHost)

	exe1Token, err := tradeExe0ForExe1(t, keyFile, exe0Token, "--vm="+box)
	if err != nil {
		t.Fatalf("trade failed: %v", err)
	}

	httpPort := Env.servers.Exeprox.HTTPPort

	t.Run("bearer_stripped", func(t *testing.T) {
		cgiURL := fmt.Sprintf("http://%s.exe.cloud:%d/cgi-bin/headers", box, httpPort)
		client := noRedirectClient(nil)
		req, err := localhostRequestWithHostHeader("GET", cgiURL, nil)
		if err != nil {
			t.Fatalf("failed to create request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+exe1Token)

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
		}

		body, _ := io.ReadAll(resp.Body)
		envMap := parseCGIEnv(body)

		// The Authorization header should have been stripped.
		if auth := envMap["HTTP_AUTHORIZATION"]; auth != "" {
			t.Errorf("expected Authorization header to be stripped, got %q", auth)
		}

		// But user identity headers should be present (proving auth worked).
		if envMap["HTTP_X_EXEDEV_USERID"] == "" {
			t.Errorf("expected X-ExeDev-UserID header to be set")
		}
	})

	t.Run("basic_auth_stripped", func(t *testing.T) {
		cgiURL := fmt.Sprintf("http://%s.exe.cloud:%d/cgi-bin/headers", box, httpPort)
		client := noRedirectClient(nil)
		req, err := localhostRequestWithHostHeader("GET", cgiURL, nil)
		if err != nil {
			t.Fatalf("failed to create request: %v", err)
		}
		req.SetBasicAuth("anyuser", exe1Token)

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
		}

		body, _ := io.ReadAll(resp.Body)
		envMap := parseCGIEnv(body)

		// The Authorization header should have been stripped.
		if auth := envMap["HTTP_AUTHORIZATION"]; auth != "" {
			t.Errorf("expected Authorization header to be stripped, got %q", auth)
		}

		// But user identity headers should be present (proving auth worked).
		if envMap["HTTP_X_EXEDEV_USERID"] == "" {
			t.Errorf("expected X-ExeDev-UserID header to be set")
		}
	})

	cleanupBox(t, keyFile, box)
}

// TestExe1TokenDeletedKeyProxy tests that an exe1 token used for proxy auth
// is rejected when the underlying SSH key has been deleted.
func TestExe1TokenDeletedKeyProxy(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 1)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	httpPort := Env.servers.Exeprox.HTTPPort

	pty, _, keyFile, _ := registerForExeDev(t)

	// Add a second SSH key — this one will back the token and then be deleted.
	// The first key stays for SSH access and cleanup.
	secondKeyPath, secondPubKey, err := testinfra.GenSSHKey(t.TempDir())
	if err != nil {
		t.Fatalf("failed to generate second key: %v", err)
	}
	pty.SendLine("ssh-key add '" + secondPubKey + "'")
	pty.Want("Added SSH key")
	pty.WantPrompt()

	box := newBox(t, pty, testinfra.BoxOpts{Command: "/bin/bash"})
	pty.Disconnect()
	waitForSSH(t, box, keyFile)

	// Start HTTP server on the box.
	serveIndex(t, box, keyFile, "deleted-key-proxy-test")

	// Configure as private route.
	configureProxyRoute(t, keyFile, box, 8080, "private")

	// Generate exe0 token with second key, scoped to the VM, trade for exe1.
	secondSigner := loadTestSigner(t, secondKeyPath)
	exe0Token := generateToken(t, secondSigner, `{}`, "v0@"+box+"."+stage.Test().BoxHost)

	exe1Token, err := tradeExe0ForExe1(t, keyFile, exe0Token, "--vm="+box)
	if err != nil {
		t.Fatalf("trade failed: %v", err)
	}

	// Verify exe1 token works for proxy auth before key deletion.
	proxyURL := fmt.Sprintf("http://%s.exe.cloud:%d/", box, httpPort)
	client := noRedirectClient(nil)
	{
		req, err := localhostRequestWithHostHeader("GET", proxyURL, nil)
		if err != nil {
			t.Fatalf("failed to create request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+exe1Token)

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 before key deletion, got %d: %s", resp.StatusCode, body)
		}
		if !strings.Contains(string(body), "deleted-key-proxy-test") {
			t.Fatalf("expected body to contain 'deleted-key-proxy-test', got: %s", body)
		}
	}

	// Delete the second SSH key (use first key for SSH auth).
	fingerprint := ssh.FingerprintSHA256(secondSigner.PublicKey())
	out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), keyFile, "ssh-key", "remove", fingerprint)
	if err != nil {
		t.Fatalf("ssh-key remove failed: %v\n%s", err, out)
	}

	// Verify exe1 token is rejected for proxy auth.
	// Poll briefly to allow the DeletedSSHKey change to propagate via gRPC.
	var lastStatus int
	var lastBody string
	for range 20 {
		req, err := localhostRequestWithHostHeader("GET", proxyURL, nil)
		if err != nil {
			t.Fatalf("failed to create request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+exe1Token)

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		lastStatus = resp.StatusCode
		lastBody = string(body)
		if resp.StatusCode == http.StatusUnauthorized {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if lastStatus != http.StatusUnauthorized {
		t.Fatalf("expected 401 after key deletion, got %d: %s", lastStatus, lastBody)
	}

	cleanupBox(t, keyFile, box)
}
