package e1e

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"exe.dev/e1e/testinfra"
	"exe.dev/stage"
	"golang.org/x/crypto/ssh"
)

// This file tests the /exec endpoint with SSH signature authentication.

var execAPINamespace = "v0@" + stage.Test().WebHost

// execAPIClient is a reusable client for making requests to the /exec endpoint.
type execAPIClient struct {
	t       *testing.T
	baseURL string
	signer  ssh.Signer
}

// newExecAPIClient creates an exec API client from a private key file.
func newExecAPIClient(t *testing.T, keyFile string) *execAPIClient {
	t.Helper()
	return &execAPIClient{
		t:       t,
		baseURL: fmt.Sprintf("http://localhost:%d", Env.HTTPPort()),
		signer:  loadTestSigner(t, keyFile),
	}
}

// generateToken generates a signed token for the /exec endpoint.
func (c *execAPIClient) generateToken(jsonPayload string) string {
	c.t.Helper()
	return generateToken(c.t, c.signer, jsonPayload, execAPINamespace)
}

// generateTokenWithNamespace generates a token with a custom namespace (for testing).
func (c *execAPIClient) generateTokenWithNamespace(jsonPayload, namespace string) string {
	c.t.Helper()
	return generateToken(c.t, c.signer, jsonPayload, namespace)
}

// exec makes a POST request to /exec with the given token and command.
func (c *execAPIClient) exec(token, command string) (*http.Response, error) {
	req, err := http.NewRequest("POST", c.baseURL+"/exec", strings.NewReader(command))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return http.DefaultClient.Do(req)
}

// TestExecAPI tests the /exec endpoint end-to-end.
func TestExecAPI(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	// Register a user via SSH first.
	pty, _, keyFile, email := registerForExeDev(t)
	pty.Disconnect()

	client := newExecAPIClient(t, keyFile)

	t.Run("whoami", func(t *testing.T) {
		token := client.generateToken(`{}`)
		resp, err := client.exec(token, "whoami")
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()

		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
		}

		output := string(body)
		if !strings.Contains(output, email) {
			t.Errorf("expected output to contain email %q, got: %s", email, output)
		}
	})

	t.Run("whoami_json", func(t *testing.T) {
		token := client.generateToken(`{}`)
		resp, err := client.exec(token, "whoami --json")
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()

		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
		}

		// Should be valid JSON
		var result map[string]any
		if err := json.Unmarshal(body, &result); err != nil {
			t.Fatalf("expected JSON output, got: %s", body)
		}
		if result["email"] != email {
			t.Errorf("expected email %q, got: %v", email, result["email"])
		}
	})

	t.Run("token_reusable", func(t *testing.T) {
		// Tokens should be reusable (no nonce enforcement unless specified).
		token := client.generateToken(`{"ctx":"reuse-test"}`)

		for i := range 3 {
			resp, err := client.exec(token, "whoami")
			if err != nil {
				t.Fatalf("request %d failed: %v", i, err)
			}
			if resp.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(resp.Body)
				t.Fatalf("request %d: expected 200, got %d: %s", i, resp.StatusCode, body)
			}
			resp.Body.Close()
		}
	})

	t.Run("unknown_command", func(t *testing.T) {
		token := client.generateToken(`{}`)
		resp, err := client.exec(token, "notarealcommand")
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()

		// Unknown commands return 404 (Not Found), not 403.
		if resp.StatusCode != http.StatusNotFound {
			body, _ := io.ReadAll(resp.Body)
			t.Errorf("expected 404, got %d: %s", resp.StatusCode, body)
		}
	})

	t.Run("missing_command", func(t *testing.T) {
		token := client.generateToken(`{}`)
		req, err := http.NewRequest("POST", client.baseURL+"/exec", nil)
		if err != nil {
			t.Fatalf("failed to create request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusBadRequest {
			body, _ := io.ReadAll(resp.Body)
			t.Errorf("expected 400, got %d: %s", resp.StatusCode, body)
		}
	})
}

// TestExecAPIExpNbf tests the exp and nbf token claims.
func TestExecAPIExpNbf(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	// Register a user via SSH first.
	pty, _, keyFile, _ := registerForExeDev(t)
	pty.Disconnect()

	client := newExecAPIClient(t, keyFile)

	t.Run("expired_token", func(t *testing.T) {
		// Token expired in the past.
		token := client.generateToken(`{"exp":1000000000}`)
		resp, err := client.exec(token, "whoami")
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusUnauthorized {
			body, _ := io.ReadAll(resp.Body)
			t.Errorf("expected 401, got %d: %s", resp.StatusCode, body)
		}
	})

	t.Run("not_yet_valid_token", func(t *testing.T) {
		// Token not valid until the future (Oct 2096, within valid range).
		token := client.generateToken(`{"nbf":4000000000}`)
		resp, err := client.exec(token, "whoami")
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusUnauthorized {
			body, _ := io.ReadAll(resp.Body)
			t.Errorf("expected 401, got %d: %s", resp.StatusCode, body)
		}
	})

	t.Run("valid_exp_and_nbf", func(t *testing.T) {
		// Token with valid exp (Oct 2096) and nbf (Sep 2001).
		token := client.generateToken(`{"exp":4000000000,"nbf":1000000000}`)
		resp, err := client.exec(token, "whoami")
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Errorf("expected 200, got %d: %s", resp.StatusCode, body)
		}
	})
}

// TestExecAPIInvalidTokens tests various invalid token scenarios.
func TestExecAPIInvalidTokens(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	// Register a user via SSH first.
	pty, _, keyFile, _ := registerForExeDev(t)
	pty.Disconnect()

	client := newExecAPIClient(t, keyFile)
	baseURL := fmt.Sprintf("http://localhost:%d", Env.HTTPPort())

	t.Run("missing_authorization_header", func(t *testing.T) {
		req, err := http.NewRequest("POST", baseURL+"/exec", strings.NewReader("whoami"))
		if err != nil {
			t.Fatalf("failed to create request: %v", err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("expected 401, got %d", resp.StatusCode)
		}
	})

	t.Run("invalid_authorization_scheme", func(t *testing.T) {
		req, err := http.NewRequest("POST", baseURL+"/exec", strings.NewReader("whoami"))
		if err != nil {
			t.Fatalf("failed to create request: %v", err)
		}
		req.Header.Set("Authorization", "Basic dXNlcjpwYXNz")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("expected 401, got %d", resp.StatusCode)
		}
	})

	t.Run("malformed_token_no_dots", func(t *testing.T) {
		req, err := http.NewRequest("POST", baseURL+"/exec", strings.NewReader("whoami"))
		if err != nil {
			t.Fatalf("failed to create request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer invalidtoken")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("expected 401, got %d", resp.StatusCode)
		}
	})

	t.Run("malformed_token_two_parts", func(t *testing.T) {
		req, err := http.NewRequest("POST", baseURL+"/exec", strings.NewReader("whoami"))
		if err != nil {
			t.Fatalf("failed to create request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer part1.part2")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("expected 401, got %d", resp.StatusCode)
		}
	})

	t.Run("invalid_base64_payload", func(t *testing.T) {
		req, err := http.NewRequest("POST", baseURL+"/exec", strings.NewReader("whoami"))
		if err != nil {
			t.Fatalf("failed to create request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer exe0.!!!invalid-base64!!!.fakesig")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("expected 401, got %d", resp.StatusCode)
		}
	})

	t.Run("invalid_json_payload", func(t *testing.T) {
		// Valid base64 but not valid JSON.
		notJSON := base64.RawURLEncoding.EncodeToString([]byte("not json"))
		req, err := http.NewRequest("POST", baseURL+"/exec", strings.NewReader("whoami"))
		if err != nil {
			t.Fatalf("failed to create request: %v", err)
		}
		req.Header.Set("Authorization", fmt.Sprintf("Bearer exe0.%s.fakesig", notJSON))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("expected 401, got %d", resp.StatusCode)
		}
	})

	t.Run("wrong_signature", func(t *testing.T) {
		// Sign with a different key. The embedded fingerprint will be for the
		// wrong key, so it won't match any registered key.
		_, wrongPrivKey, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatalf("failed to generate key: %v", err)
		}
		wrongSigner, err := ssh.NewSignerFromKey(wrongPrivKey)
		if err != nil {
			t.Fatalf("failed to create signer: %v", err)
		}

		payload := []byte(`{}`)
		payloadB64 := base64.RawURLEncoding.EncodeToString(payload)
		wrongSigBlob := createSigBlob(t, wrongSigner, payload, execAPINamespace)
		token := "exe0." + payloadB64 + "." + wrongSigBlob

		resp, err := client.exec(token, "whoami")
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusUnauthorized {
			body, _ := io.ReadAll(resp.Body)
			t.Errorf("expected 401, got %d: %s", resp.StatusCode, body)
		}
	})

	t.Run("tampered_payload", func(t *testing.T) {
		// Sign one payload but send a different one in the token.
		originalPayload := []byte(`{"ctx":"original"}`)
		sigBlob := createSigBlob(t, client.signer, originalPayload, execAPINamespace)

		tamperedPayload := []byte(`{"ctx":"tampered"}`)
		tamperedB64 := base64.RawURLEncoding.EncodeToString(tamperedPayload)
		token := "exe0." + tamperedB64 + "." + sigBlob

		resp, err := client.exec(token, "whoami")
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusUnauthorized {
			body, _ := io.ReadAll(resp.Body)
			t.Errorf("expected 401, got %d: %s", resp.StatusCode, body)
		}
	})

	t.Run("wrong_namespace", func(t *testing.T) {
		// Sign with the wrong namespace.
		token := client.generateTokenWithNamespace(`{}`, "wrong@namespace")
		resp, err := client.exec(token, "whoami")
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusUnauthorized {
			body, _ := io.ReadAll(resp.Body)
			t.Errorf("expected 401, got %d: %s", resp.StatusCode, body)
		}
	})

	t.Run("method_not_allowed", func(t *testing.T) {
		req, err := http.NewRequest("GET", baseURL+"/exec", nil)
		if err != nil {
			t.Fatalf("failed to create request: %v", err)
		}
		token := client.generateToken(`{}`)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("expected 405, got %d", resp.StatusCode)
		}
	})
}

// TestExecAPIWithSecondKey tests that a second SSH key added to the account
// can also be used to authenticate to the /exec endpoint.
func TestExecAPIWithSecondKey(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	// Register a user via SSH first.
	pty, _, keyFile, email := registerForExeDev(t)

	// Generate a second SSH key.
	secondKeyPath, secondPubKey, err := testinfra.GenSSHKey(t.TempDir())
	if err != nil {
		t.Fatalf("failed to generate second key: %v", err)
	}

	// Add the second key via SSH.
	pty.SendLine("ssh-key add '" + secondPubKey + "'")
	pty.Want("Added SSH key")
	pty.WantPrompt()
	pty.Disconnect()

	// Both keys should work for /exec.
	client1 := newExecAPIClient(t, keyFile)
	client2 := newExecAPIClient(t, secondKeyPath)

	// Test first key.
	token1 := client1.generateToken(`{}`)
	resp1, err := client1.exec(token1, "whoami")
	if err != nil {
		t.Fatalf("first key request failed: %v", err)
	}
	body1, _ := io.ReadAll(resp1.Body)
	resp1.Body.Close()
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("first key: expected 200, got %d: %s", resp1.StatusCode, body1)
	}
	if !strings.Contains(string(body1), email) {
		t.Errorf("first key: expected output to contain email %q, got: %s", email, body1)
	}

	// Test second key.
	token2 := client2.generateToken(`{}`)
	resp2, err := client2.exec(token2, "whoami")
	if err != nil {
		t.Fatalf("second key request failed: %v", err)
	}
	body2, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("second key: expected 200, got %d: %s", resp2.StatusCode, body2)
	}
	if !strings.Contains(string(body2), email) {
		t.Errorf("second key: expected output to contain email %q, got: %s", email, body2)
	}

	// Clean up: remove second key.
	pty2 := sshToExeDev(t, keyFile)
	pty2.SendLine("ssh-key remove '" + strings.TrimSpace(secondPubKey) + "'")
	pty2.Want("Deleted SSH key")
	pty2.WantPrompt()
	pty2.Disconnect()

	// After removal, second key should no longer work.
	resp3, err := client2.exec(token2, "whoami")
	if err != nil {
		t.Fatalf("request after key removal failed: %v", err)
	}
	defer resp3.Body.Close()

	if resp3.StatusCode != http.StatusUnauthorized {
		body, _ := io.ReadAll(resp3.Body)
		t.Errorf("expected 401 after key removal, got %d: %s", resp3.StatusCode, body)
	}
}

// TestExecAPIUnregisteredKey tests that an unregistered key cannot access /exec.
func TestExecAPIUnregisteredKey(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	// Generate a new key that is NOT registered.
	keyPath, _, err := testinfra.GenSSHKey(t.TempDir())
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}

	client := newExecAPIClient(t, keyPath)
	token := client.generateToken(`{}`)

	resp, err := client.exec(token, "whoami")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("expected 401 for unregistered key, got %d: %s", resp.StatusCode, body)
	}
}

// TestExecAPISigBlobErrors tests various signature blob format errors.
func TestExecAPISigBlobErrors(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, _, _, _ := registerForExeDev(t)
	pty.Disconnect()

	baseURL := fmt.Sprintf("http://localhost:%d", Env.HTTPPort())

	t.Run("invalid_sigblob_base64", func(t *testing.T) {
		payload := []byte(`{}`)
		payloadB64 := base64.RawURLEncoding.EncodeToString(payload)
		token := "exe0." + payloadB64 + ".!!!not-valid-base64!!!"

		req, err := http.NewRequest("POST", baseURL+"/exec", strings.NewReader("whoami"))
		if err != nil {
			t.Fatalf("failed to create request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusUnauthorized {
			body, _ := io.ReadAll(resp.Body)
			t.Errorf("expected 401, got %d: %s", resp.StatusCode, body)
		}
	})

	t.Run("empty_sigblob", func(t *testing.T) {
		payload := []byte(`{}`)
		payloadB64 := base64.RawURLEncoding.EncodeToString(payload)
		token := "exe0." + payloadB64 + "."

		req, err := http.NewRequest("POST", baseURL+"/exec", strings.NewReader("whoami"))
		if err != nil {
			t.Fatalf("failed to create request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusUnauthorized {
			body, _ := io.ReadAll(resp.Body)
			t.Errorf("expected 401, got %d: %s", resp.StatusCode, body)
		}
	})

	t.Run("not_sshsig_blob", func(t *testing.T) {
		payload := []byte(`{}`)
		payloadB64 := base64.RawURLEncoding.EncodeToString(payload)
		fakeSig := base64.RawURLEncoding.EncodeToString([]byte("not an SSHSIG blob"))
		token := "exe0." + payloadB64 + "." + fakeSig

		req, err := http.NewRequest("POST", baseURL+"/exec", strings.NewReader("whoami"))
		if err != nil {
			t.Fatalf("failed to create request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusUnauthorized {
			body, _ := io.ReadAll(resp.Body)
			t.Errorf("expected 401, got %d: %s", resp.StatusCode, body)
		}
	})
}

// TestExecAPINamespaceIsolation verifies that tokens signed for one namespace
// cannot be used for a different namespace. This is critical for security:
// - A VM token (v0@vmname.exe.xyz) must NOT work for the /exec API
// - An API token must NOT work for VM access
// - A token for VM1 must NOT work for VM2
func TestExecAPINamespaceIsolation(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	// Register a user via SSH first.
	pty, _, keyFile, _ := registerForExeDev(t)
	pty.Disconnect()

	client := newExecAPIClient(t, keyFile)

	t.Run("vm_token_rejected_by_exec_api", func(t *testing.T) {
		// A token signed for a VM namespace should NOT work for /exec
		vmToken := client.generateTokenWithNamespace(`{}`, "v0@myvm."+stage.Test().BoxHost)
		resp, err := client.exec(vmToken, "whoami")
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusUnauthorized {
			body, _ := io.ReadAll(resp.Body)
			t.Errorf("VM token should be rejected by /exec API: expected 401, got %d: %s", resp.StatusCode, body)
		}
	})

	t.Run("other_vm_token_rejected", func(t *testing.T) {
		// A token for vm1 should NOT work when validated against vm2's namespace
		// (This tests the namespace comparison logic)
		vm1Token := client.generateTokenWithNamespace(`{}`, "v0@vm1."+stage.Test().BoxHost)

		// Try to use vm1's token - it should fail for /exec
		resp, err := client.exec(vm1Token, "whoami")
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusUnauthorized {
			body, _ := io.ReadAll(resp.Body)
			t.Errorf("VM1 token should be rejected: expected 401, got %d: %s", resp.StatusCode, body)
		}
	})

	t.Run("valid_api_token_works", func(t *testing.T) {
		// Sanity check: a properly namespaced token should work
		apiToken := client.generateTokenWithNamespace(`{}`, execAPINamespace)
		resp, err := client.exec(apiToken, "whoami")
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Errorf("valid API token should work: expected 200, got %d: %s", resp.StatusCode, body)
		}
	})

	t.Run("subtle_namespace_mismatch", func(t *testing.T) {
		// Test that subtle variations in namespace are rejected
		subtleVariations := []string{
			execAPINamespace + " ",                             // trailing space
			" " + execAPINamespace,                             // leading space
			strings.ToUpper(execAPINamespace),                  // wrong case
			strings.Replace(execAPINamespace, "v0@", "v2@", 1), // wrong version
			execAPINamespace + "x",                             // extra character
			strings.Replace(execAPINamespace, "@", "", 1),      // missing @
		}

		for _, namespace := range subtleVariations {
			token := client.generateTokenWithNamespace(`{}`, namespace)
			resp, err := client.exec(token, "whoami")
			if err != nil {
				t.Fatalf("request failed for namespace %q: %v", namespace, err)
			}

			if resp.StatusCode != http.StatusUnauthorized {
				body, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				t.Errorf("token with namespace %q should be rejected: expected 401, got %d: %s",
					namespace, resp.StatusCode, body)
			} else {
				resp.Body.Close()
			}
		}
	})
}

// TestExecAPISignatureBitFlip tests that any bit flip in the signature is detected.
func TestExecAPISignatureBitFlip(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	// Register a user via SSH first.
	pty, _, keyFile, _ := registerForExeDev(t)
	pty.Disconnect()

	client := newExecAPIClient(t, keyFile)

	// Generate a valid token.
	payload := []byte(`{"ctx":"bitflip"}`)
	payloadB64 := base64.RawURLEncoding.EncodeToString(payload)
	validSigBlob := createSigBlob(t, client.signer, payload, execAPINamespace)
	validToken := "exe0." + payloadB64 + "." + validSigBlob

	// First verify the valid token works.
	resp, err := client.exec(validToken, "whoami")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected valid token to work, got %d", resp.StatusCode)
	}

	// Decode the sig blob, flip a bit near the end (in the signature portion),
	// re-encode, and verify the token is rejected.
	sigBytes, err := base64.RawURLEncoding.DecodeString(validSigBlob)
	if err != nil {
		t.Fatalf("failed to decode sig blob: %v", err)
	}

	corruptedBytes := make([]byte, len(sigBytes))
	copy(corruptedBytes, sigBytes)
	corruptedBytes[len(corruptedBytes)-10] ^= 0x01

	corruptedSigBlob := base64.RawURLEncoding.EncodeToString(corruptedBytes)
	corruptedToken := "exe0." + payloadB64 + "." + corruptedSigBlob

	resp2, err := client.exec(corruptedToken, "whoami")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp2.Body.Close()

	if resp2.StatusCode != http.StatusUnauthorized {
		body, _ := io.ReadAll(resp2.Body)
		t.Errorf("expected 401 for bit-flipped signature, got %d: %s", resp2.StatusCode, body)
	}
}

// TestExecAPIStrictJSONValidation tests that the /exec endpoint properly validates
// token payload JSON according to the strict parsing rules.
func TestExecAPIStrictJSONValidation(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	// Register a user via SSH first.
	pty, _, keyFile, _ := registerForExeDev(t)
	pty.Disconnect()

	client := newExecAPIClient(t, keyFile)
	baseURL := fmt.Sprintf("http://localhost:%d", Env.HTTPPort())

	tests := []struct {
		name       string
		payload    string
		wantStatus int
	}{
		// Invalid cases - should return 401
		{"newline", "{\n\"exp\":2000000000}", http.StatusUnauthorized},
		{"carriage_return", "{\r\"exp\":2000000000}", http.StatusUnauthorized},
		{"duplicate_exp", `{"exp":2000000000,"exp":2100000000}`, http.StatusUnauthorized},
		{"duplicate_in_ctx", `{"ctx":{"a":1,"a":2}}`, http.StatusUnauthorized},
		{"unknown_key", `{"foo":"bar"}`, http.StatusUnauthorized},
		{"exp_decimal", `{"exp":2000000000.0}`, http.StatusUnauthorized},
		{"exp_exponent", `{"exp":2e9}`, http.StatusUnauthorized},
		{"exp_out_of_range_low", `{"exp":100}`, http.StatusUnauthorized},
		{"exp_out_of_range_high", `{"exp":9999999999}`, http.StatusUnauthorized},
		{"nbf_out_of_range", `{"nbf":100}`, http.StatusUnauthorized},

		// Valid cases - should return 200
		{"valid_empty", `{}`, http.StatusOK},
		{"valid_with_ctx", `{"ctx":{"nested":1}}`, http.StatusOK},
		{"valid_with_exp_nbf", `{"exp":2000000000,"nbf":1000000000}`, http.StatusOK},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Generate a signed token with the raw payload.
			token := generateToken(t, client.signer, tt.payload, execAPINamespace)

			req, err := http.NewRequest("POST", baseURL+"/exec", strings.NewReader("whoami"))
			if err != nil {
				t.Fatalf("failed to create request: %v", err)
			}
			req.Header.Set("Authorization", "Bearer "+token)

			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != tt.wantStatus {
				body, _ := io.ReadAll(resp.Body)
				t.Errorf("expected %d, got %d: %s", tt.wantStatus, resp.StatusCode, body)
			}
		})
	}
}

// TestExecAPICmds tests that the cmds token field correctly restricts which commands
// can be executed via the /exec endpoint.
func TestExecAPICmds(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, _, keyFile, _ := registerForExeDev(t)
	pty.Disconnect()

	client := newExecAPIClient(t, keyFile)

	t.Run("cmds_restricts_to_allowed", func(t *testing.T) {
		token := client.generateToken(`{"cmds":["whoami"]}`)

		// whoami should work
		resp, err := client.exec(token, "whoami")
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("expected 200 for whoami, got %d: %s", resp.StatusCode, body)
		}

		// ls should be blocked
		resp2, err := client.exec(token, "ls")
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp2.Body.Close()
		if resp2.StatusCode != http.StatusForbidden {
			body, _ := io.ReadAll(resp2.Body)
			t.Errorf("expected 403 for ls, got %d: %s", resp2.StatusCode, body)
		}
	})

	t.Run("empty_cmds_blocks_all", func(t *testing.T) {
		token := client.generateToken(`{"cmds":[]}`)

		resp, err := client.exec(token, "whoami")
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			body, _ := io.ReadAll(resp.Body)
			t.Errorf("expected 403 for empty cmds, got %d: %s", resp.StatusCode, body)
		}
	})

	t.Run("default_cmds_allow_standard", func(t *testing.T) {
		// Token with no cmds field should use DefaultTokenCmds.
		// We verify each command is allowed (not 403). Some commands like
		// "share show" return 422 because they need arguments, but that still
		// means the command was permitted by cmds enforcement.
		token := client.generateToken(`{}`)

		for _, cmd := range []string{"whoami", "ls", "ssh-key list", "share show"} {
			resp, err := client.exec(token, cmd)
			if err != nil {
				t.Fatalf("request for %q failed: %v", cmd, err)
			}
			if resp.StatusCode == http.StatusForbidden {
				body, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				t.Errorf("expected %q to be allowed with default cmds, got 403: %s", cmd, body)
			} else {
				resp.Body.Close()
			}
		}
	})

	t.Run("subcommand_exact_match", func(t *testing.T) {
		token := client.generateToken(`{"cmds":["ssh-key list"]}`)

		// ssh-key list should work
		resp, err := client.exec(token, "ssh-key list")
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("expected 200 for ssh-key list, got %d: %s", resp.StatusCode, body)
		}

		// ssh-key add should be blocked
		resp2, err := client.exec(token, "ssh-key add fakepubkey")
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp2.Body.Close()
		if resp2.StatusCode != http.StatusForbidden {
			body, _ := io.ReadAll(resp2.Body)
			t.Errorf("expected 403 for ssh-key add, got %d: %s", resp2.StatusCode, body)
		}
	})

	t.Run("parent_does_not_grant_subcommands", func(t *testing.T) {
		// "ssh-key" alone does NOT grant "ssh-key list"
		token := client.generateToken(`{"cmds":["ssh-key"]}`)

		resp, err := client.exec(token, "ssh-key list")
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			body, _ := io.ReadAll(resp.Body)
			t.Errorf("expected 403 for ssh-key list with only ssh-key granted, got %d: %s", resp.StatusCode, body)
		}
	})
}

// TestExecAPILargeTokenRejected tests that tokens exceeding MaxTokenSize (8KB) are rejected.
func TestExecAPILargeTokenRejected(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, _, keyFile, _ := registerForExeDev(t)
	pty.Disconnect()

	client := newExecAPIClient(t, keyFile)

	// Build a ctx payload large enough to push the token over 8KB (MaxTokenSize=8192).
	// The payload gets base64-encoded (4/3 expansion), so ~6200 raw chars → ~8280 base64
	// chars, plus prefix (5), sigblob (~240), and dots = well over 8KB.
	bigCtx := strings.Repeat("x", 6200)
	payload := fmt.Sprintf(`{"ctx":"%s"}`, bigCtx)
	token := client.generateToken(payload)

	resp, err := client.exec(token, "whoami")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("expected 401 for oversized token, got %d: %s", resp.StatusCode, body)
	}
}
