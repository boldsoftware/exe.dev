package execore

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"exe.dev/sqlite"
	"github.com/hiddeco/sshsig"
	"golang.org/x/crypto/ssh"
)

func TestExecHandler(t *testing.T) {
	t.Parallel()

	s := newTestServer(t)

	// Generate a test ed25519 key pair.
	pubKey, privKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}

	// Convert to SSH format.
	sshPubKey, err := ssh.NewPublicKey(pubKey)
	if err != nil {
		t.Fatalf("failed to create SSH public key: %v", err)
	}
	sshPrivKey, err := ssh.NewSignerFromKey(privKey)
	if err != nil {
		t.Fatalf("failed to create SSH signer: %v", err)
	}

	// Register a user and add the SSH key.
	ctx := t.Context()
	userID := "usr" + generateRegistrationToken()
	email := "test@example.com"
	pubKeyStr := string(ssh.MarshalAuthorizedKey(sshPubKey))
	fingerprint := strings.TrimPrefix(ssh.FingerprintSHA256(sshPubKey), "SHA256:")
	err = s.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		if _, err := tx.Exec(`INSERT INTO users (user_id, email) VALUES (?, ?)`, userID, email); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO ssh_keys (user_id, public_key, fingerprint) VALUES (?, ?, ?)`, userID, pubKeyStr, fingerprint); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatalf("failed to create user and SSH key: %v", err)
	}

	// Create a token.
	execNS := "v0@" + s.env.WebHost
	payload := []byte(`{}`)
	payloadB64 := base64.RawURLEncoding.EncodeToString(payload)
	sigBlob := createSigBlob(t, sshPrivKey, payload, execNS)
	token := "exe0." + payloadB64 + "." + sigBlob

	// Test: whoami command returns output containing the user email.
	t.Run("whoami", func(t *testing.T) {
		req, err := http.NewRequest("POST", s.httpURL()+"/exec", strings.NewReader("whoami"))
		if err != nil {
			t.Fatalf("failed to create request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Host = s.env.WebHost

		resp, err := http.DefaultClient.Do(req)
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
			t.Errorf("whoami output should contain email %q, got: %s", email, output)
		}
	})

	// Test: missing command body returns 400.
	t.Run("missing_command", func(t *testing.T) {
		req, err := http.NewRequest("POST", s.httpURL()+"/exec", nil)
		if err != nil {
			t.Fatalf("failed to create request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Host = s.env.WebHost

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusBadRequest {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("expected 400, got %d: %s", resp.StatusCode, body)
		}
	})

	// Test: unknown command returns 404.
	t.Run("unknown_command", func(t *testing.T) {
		req, err := http.NewRequest("POST", s.httpURL()+"/exec", strings.NewReader("notacommand"))
		if err != nil {
			t.Fatalf("failed to create request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Host = s.env.WebHost

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusNotFound {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("expected 404, got %d: %s", resp.StatusCode, body)
		}
	})

	// Test: null byte in body returns 400.
	t.Run("null_byte_in_body", func(t *testing.T) {
		req, err := http.NewRequest("POST", s.httpURL()+"/exec", strings.NewReader("whoami\x00rm myvm"))
		if err != nil {
			t.Fatalf("failed to create request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Host = s.env.WebHost

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusBadRequest {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("expected 400, got %d: %s", resp.StatusCode, body)
		}
	})

	// Test: lowercase "bearer" is accepted (RFC 7235).
	t.Run("bearer_case_insensitive", func(t *testing.T) {
		req, err := http.NewRequest("POST", s.httpURL()+"/exec", strings.NewReader("whoami"))
		if err != nil {
			t.Fatalf("failed to create request: %v", err)
		}
		req.Header.Set("Authorization", "bearer "+token)
		req.Host = s.env.WebHost

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()

		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 for lowercase bearer, got %d: %s", resp.StatusCode, body)
		}
		if !strings.Contains(string(body), email) {
			t.Errorf("whoami output should contain email %q, got: %s", email, body)
		}
	})
}

func TestExecHandlerExpNbf(t *testing.T) {
	t.Parallel()

	s := newTestServer(t)

	// Generate a test ed25519 key pair.
	pubKey, privKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}

	// Convert to SSH format.
	sshPubKey, err := ssh.NewPublicKey(pubKey)
	if err != nil {
		t.Fatalf("failed to create SSH public key: %v", err)
	}
	sshPrivKey, err := ssh.NewSignerFromKey(privKey)
	if err != nil {
		t.Fatalf("failed to create SSH signer: %v", err)
	}

	// Register a user and add the SSH key.
	ctx := t.Context()
	userID := "usr" + generateRegistrationToken()
	pubKeyStr := string(ssh.MarshalAuthorizedKey(sshPubKey))
	fingerprint := strings.TrimPrefix(ssh.FingerprintSHA256(sshPubKey), "SHA256:")
	err = s.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		if _, err := tx.Exec(`INSERT INTO users (user_id, email) VALUES (?, ?)`, userID, "expnbf@example.com"); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO ssh_keys (user_id, public_key, fingerprint) VALUES (?, ?, ?)`, userID, pubKeyStr, fingerprint); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatalf("failed to create user and SSH key: %v", err)
	}

	execNS := "v0@" + s.env.WebHost
	makeToken := func(payloadJSON string) string {
		payload := []byte(payloadJSON)
		payloadB64 := base64.RawURLEncoding.EncodeToString(payload)
		sigBlob := createSigBlob(t, sshPrivKey, payload, execNS)
		return "exe0." + payloadB64 + "." + sigBlob
	}

	t.Run("expired_token", func(t *testing.T) {
		// Token expired in the past.
		token := makeToken(`{"exp": 1000000000}`)
		req, err := http.NewRequest("POST", s.httpURL()+"/exec", strings.NewReader("whoami"))
		if err != nil {
			t.Fatalf("failed to create request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Host = s.env.WebHost

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusUnauthorized {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("expected 401, got %d: %s", resp.StatusCode, body)
		}
		body, _ := io.ReadAll(resp.Body)
		if !strings.Contains(string(body), "expired") {
			t.Errorf("expected error to mention 'expired', got: %s", body)
		}
	})

	t.Run("not_yet_valid_token", func(t *testing.T) {
		// Token not valid until the future (Oct 2096, within valid range).
		token := makeToken(`{"nbf": 4000000000}`)
		req, err := http.NewRequest("POST", s.httpURL()+"/exec", strings.NewReader("whoami"))
		if err != nil {
			t.Fatalf("failed to create request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Host = s.env.WebHost

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusUnauthorized {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("expected 401, got %d: %s", resp.StatusCode, body)
		}
		body, _ := io.ReadAll(resp.Body)
		if !strings.Contains(string(body), "not yet valid") {
			t.Errorf("expected error to mention 'not yet valid', got: %s", body)
		}
	})

	t.Run("valid_exp_and_nbf", func(t *testing.T) {
		// Token with valid exp (Oct 2096) and nbf (Sep 2001).
		token := makeToken(`{"exp": 4000000000, "nbf": 1000000000}`)
		req, err := http.NewRequest("POST", s.httpURL()+"/exec", strings.NewReader("whoami"))
		if err != nil {
			t.Fatalf("failed to create request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Host = s.env.WebHost

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
		}
	})
}

func TestExecHandlerInvalidSignature(t *testing.T) {
	t.Parallel()

	s := newTestServer(t)

	// Generate a test ed25519 key pair (registered in DB).
	pubKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}

	// Generate a different key for signing (to create a token with unregistered fingerprint).
	_, wrongPrivKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate wrong key: %v", err)
	}

	// Convert to SSH format.
	sshPubKey, err := ssh.NewPublicKey(pubKey)
	if err != nil {
		t.Fatalf("failed to create SSH public key: %v", err)
	}
	wrongSshPrivKey, err := ssh.NewSignerFromKey(wrongPrivKey)
	if err != nil {
		t.Fatalf("failed to create SSH signer: %v", err)
	}

	// Register a user and add the SSH key.
	ctx := t.Context()
	userID := "usr" + generateRegistrationToken()
	pubKeyStr := string(ssh.MarshalAuthorizedKey(sshPubKey))
	fingerprint := strings.TrimPrefix(ssh.FingerprintSHA256(sshPubKey), "SHA256:")
	err = s.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		if _, err := tx.Exec(`INSERT INTO users (user_id, email) VALUES (?, ?)`, userID, "test2@example.com"); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO ssh_keys (user_id, public_key, fingerprint) VALUES (?, ?, ?)`, userID, pubKeyStr, fingerprint); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatalf("failed to create user and SSH key: %v", err)
	}

	// Create a token signed by the wrong key. In the new token format, the fingerprint
	// is derived from the signing key embedded in the SSHSIG blob, so this token will
	// have the wrong key's fingerprint (not found in DB) rather than a signature mismatch.
	execNS := "v0@" + s.env.WebHost
	payload := []byte(`{}`)
	payloadB64 := base64.RawURLEncoding.EncodeToString(payload)
	sigBlob := createSigBlob(t, wrongSshPrivKey, payload, execNS)
	token := "exe0." + payloadB64 + "." + sigBlob

	// Make request.
	req, err := http.NewRequest("POST", s.httpURL()+"/exec", nil)
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Host = s.env.WebHost

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 401, got %d: %s", resp.StatusCode, body)
	}
}

// createSigBlob creates a base64url-encoded SSHSIG blob from a signer and message.
func createSigBlob(t *testing.T, signer ssh.Signer, message []byte, namespace string) string {
	t.Helper()

	sig, err := sshsig.Sign(bytes.NewReader(message), signer, sshsig.HashSHA512, namespace)
	if err != nil {
		t.Fatalf("failed to sign: %v", err)
	}

	return base64.RawURLEncoding.EncodeToString(sig.Marshal())
}

// TestValidateTokenNamespace tests the validateToken function with different namespaces.
// This is an integration test that verifies the full flow including database lookup.
func TestValidateTokenNamespace(t *testing.T) {
	t.Parallel()

	s := newTestServer(t)

	// Generate a test ed25519 key pair.
	pubKey, privKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}

	// Convert to SSH format.
	sshPubKey, err := ssh.NewPublicKey(pubKey)
	if err != nil {
		t.Fatalf("failed to create SSH public key: %v", err)
	}
	sshPrivKey, err := ssh.NewSignerFromKey(privKey)
	if err != nil {
		t.Fatalf("failed to create SSH signer: %v", err)
	}

	// Register a user and add the SSH key.
	ctx := t.Context()
	userID := "usr" + generateRegistrationToken()
	email := "namespace@example.com"
	pubKeyStr := string(ssh.MarshalAuthorizedKey(sshPubKey))
	fingerprint := strings.TrimPrefix(ssh.FingerprintSHA256(sshPubKey), "SHA256:")
	err = s.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		if _, err := tx.Exec(`INSERT INTO users (user_id, email) VALUES (?, ?)`, userID, email); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO ssh_keys (user_id, public_key, fingerprint) VALUES (?, ?, ?)`, userID, pubKeyStr, fingerprint); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatalf("failed to create user and SSH key: %v", err)
	}

	execNS := "v0@" + s.env.WebHost

	makeToken := func(namespace string, payloadJSON map[string]any) string {
		payload, err := json.Marshal(payloadJSON)
		if err != nil {
			t.Fatalf("failed to marshal payload: %v", err)
		}
		payloadB64 := base64.RawURLEncoding.EncodeToString(payload)
		sigBlob := createSigBlob(t, sshPrivKey, payload, namespace)
		return "exe0." + payloadB64 + "." + sigBlob
	}

	t.Run("correct_namespace_api", func(t *testing.T) {
		token := makeToken(execNS, map[string]any{})
		result, err := s.validateToken(ctx, token, execNS)
		if err != nil {
			t.Fatalf("validateToken failed: %v", err)
		}
		if result.UserID != userID {
			t.Errorf("expected userID %q, got %q", userID, result.UserID)
		}
	})

	t.Run("correct_namespace_vm", func(t *testing.T) {
		// Test VM-specific namespace: v0@myvm.<BoxHost>
		vmNamespace := "v0@myvm." + s.env.BoxHost
		token := makeToken(vmNamespace, map[string]any{"ctx": "data"})
		result, err := s.validateToken(ctx, token, vmNamespace)
		if err != nil {
			t.Fatalf("validateToken failed: %v", err)
		}
		if result.UserID != userID {
			t.Errorf("expected userID %q, got %q", userID, result.UserID)
		}
		if result.Payload["ctx"] != "data" {
			t.Errorf("expected payload ctx=data, got %v", result.Payload)
		}
	})

	t.Run("wrong_namespace", func(t *testing.T) {
		// Token signed for API namespace, but we validate with VM namespace
		token := makeToken(execNS, map[string]any{})
		_, err := s.validateToken(ctx, token, "v0@myvm."+s.env.BoxHost)
		if err == nil {
			t.Fatal("expected validateToken to fail with wrong namespace")
		}
		if !strings.Contains(err.Error(), "invalid token") {
			t.Errorf("expected generic invalid token error, got: %v", err)
		}
	})

	t.Run("vm_token_wrong_vm", func(t *testing.T) {
		// Token signed for vm1, but we validate for vm2
		token := makeToken("v0@vm1."+s.env.BoxHost, map[string]any{})
		_, err := s.validateToken(ctx, token, "v0@vm2."+s.env.BoxHost)
		if err == nil {
			t.Fatal("expected validateToken to fail for different VM")
		}
	})

	t.Run("ctx_raw_preserved_verbatim", func(t *testing.T) {
		// Test that CtxRaw preserves the exact bytes of the ctx field,
		// including unusual formatting that would not survive a JSON round-trip.
		// This is critical for the proxy to pass ctx verbatim to VMs.
		//
		// The ctx field here is a JSON object with weird whitespace (tabs, extra spaces).
		// The header should receive ONLY this value, not the outer payload.
		// Note: newlines are forbidden by the token format spec.
		weirdCtx := `{"foo":   "bar",	"n": 1}`
		payload := []byte(`{"exp":4000000000,"ctx":` + weirdCtx + `}`)
		payloadB64 := base64.RawURLEncoding.EncodeToString(payload)
		sigBlob := createSigBlob(t, sshPrivKey, payload, execNS)
		token := "exe0." + payloadB64 + "." + sigBlob

		result, err := s.validateToken(ctx, token, execNS)
		if err != nil {
			t.Fatalf("validateToken failed: %v", err)
		}

		// CtxRaw must be byte-for-byte identical to the ctx field value
		if string(result.CtxRaw) != weirdCtx {
			t.Errorf("CtxRaw not preserved verbatim\nexpected: %q\nreceived: %q",
				weirdCtx, string(result.CtxRaw))
		}
	})

	t.Run("ctx_raw_nil_when_absent", func(t *testing.T) {
		// When ctx field is not present, CtxRaw should be nil
		payload := []byte(`{"exp":4000000000}`)
		payloadB64 := base64.RawURLEncoding.EncodeToString(payload)
		sigBlob := createSigBlob(t, sshPrivKey, payload, execNS)
		token := "exe0." + payloadB64 + "." + sigBlob

		result, err := s.validateToken(ctx, token, execNS)
		if err != nil {
			t.Fatalf("validateToken failed: %v", err)
		}

		if result.CtxRaw != nil {
			t.Errorf("expected CtxRaw to be nil when ctx absent, got: %q", string(result.CtxRaw))
		}
	})

	t.Run("cmds_populated_from_token", func(t *testing.T) {
		payload, _ := json.Marshal(map[string]any{"cmds": []string{"whoami", "ls"}})
		payloadB64 := base64.RawURLEncoding.EncodeToString(payload)
		sigBlob := createSigBlob(t, sshPrivKey, payload, execNS)
		token := "exe0." + payloadB64 + "." + sigBlob

		result, err := s.validateToken(ctx, token, execNS)
		if err != nil {
			t.Fatalf("validateToken failed: %v", err)
		}

		if len(result.Cmds) != 2 || result.Cmds[0] != "whoami" || result.Cmds[1] != "ls" {
			t.Errorf("expected cmds [whoami ls], got: %v", result.Cmds)
		}
	})

	t.Run("cmds_nil_when_omitted", func(t *testing.T) {
		payload := []byte(`{}`)
		payloadB64 := base64.RawURLEncoding.EncodeToString(payload)
		sigBlob := createSigBlob(t, sshPrivKey, payload, execNS)
		token := "exe0." + payloadB64 + "." + sigBlob

		result, err := s.validateToken(ctx, token, execNS)
		if err != nil {
			t.Fatalf("validateToken failed: %v", err)
		}

		if result.Cmds != nil {
			t.Errorf("expected nil cmds when omitted, got: %v", result.Cmds)
		}
	})
}

func TestTokenCmdsAllow(t *testing.T) {
	t.Run("nil_cmds_uses_default", func(t *testing.T) {
		// nil cmds should allow default commands
		if !tokenCmdsAllow(nil, "ls") {
			t.Error("expected ls to be allowed with nil cmds")
		}
		if !tokenCmdsAllow(nil, "whoami") {
			t.Error("expected whoami to be allowed with nil cmds")
		}
		if !tokenCmdsAllow(nil, "new") {
			t.Error("expected new to be allowed with nil cmds")
		}
		if !tokenCmdsAllow(nil, "ssh-key list") {
			t.Error("expected ssh-key list to be allowed with nil cmds")
		}
		if !tokenCmdsAllow(nil, "share show") {
			t.Error("expected share show to be allowed with nil cmds")
		}
		// ssh-key add is NOT in default list
		if tokenCmdsAllow(nil, "ssh-key add") {
			t.Error("expected ssh-key add to be blocked with nil cmds")
		}
		// rm is NOT in default list
		if tokenCmdsAllow(nil, "rm") {
			t.Error("expected rm to be blocked with nil cmds")
		}
	})

	t.Run("empty_cmds_blocks_all", func(t *testing.T) {
		empty := []string{}
		if tokenCmdsAllow(empty, "ls") {
			t.Error("expected ls to be blocked with empty cmds")
		}
		if tokenCmdsAllow(empty, "whoami") {
			t.Error("expected whoami to be blocked with empty cmds")
		}
	})

	t.Run("specific_cmds", func(t *testing.T) {
		cmds := []string{"whoami"}
		if !tokenCmdsAllow(cmds, "whoami") {
			t.Error("expected whoami to be allowed")
		}
		if tokenCmdsAllow(cmds, "ls") {
			t.Error("expected ls to be blocked")
		}
	})

	t.Run("subcommand_matching", func(t *testing.T) {
		cmds := []string{"ssh-key list"}
		if !tokenCmdsAllow(cmds, "ssh-key list") {
			t.Error("expected ssh-key list to be allowed")
		}
		if tokenCmdsAllow(cmds, "ssh-key add") {
			t.Error("expected ssh-key add to be blocked")
		}
		if tokenCmdsAllow(cmds, "ssh-key") {
			t.Error("expected bare ssh-key to be blocked")
		}
	})

	t.Run("parent_does_not_grant_subcommands", func(t *testing.T) {
		// A parent command like "ssh-key" does NOT grant access to subcommands.
		// You must explicitly list each subcommand. This is for forward-compat:
		// adding new subcommands should not suddenly expose tokens to new risks.
		cmds := []string{"ssh-key"}
		if !tokenCmdsAllow(cmds, "ssh-key") {
			t.Error("expected bare ssh-key to be allowed")
		}
		if tokenCmdsAllow(cmds, "ssh-key list") {
			t.Error("expected ssh-key list to be blocked when only ssh-key is in cmds")
		}
		if tokenCmdsAllow(cmds, "ssh-key add") {
			t.Error("expected ssh-key add to be blocked when only ssh-key is in cmds")
		}
	})

	t.Run("empty_resolved_cmd", func(t *testing.T) {
		if tokenCmdsAllow(nil, "") {
			t.Error("expected empty resolvedCmd to be blocked")
		}
	})
}

func TestExecHandlerCmds(t *testing.T) {
	t.Parallel()

	s := newTestServer(t)

	// Generate a test ed25519 key pair.
	pubKey, privKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}

	// Convert to SSH format.
	sshPubKey, err := ssh.NewPublicKey(pubKey)
	if err != nil {
		t.Fatalf("failed to create SSH public key: %v", err)
	}
	sshPrivKey, err := ssh.NewSignerFromKey(privKey)
	if err != nil {
		t.Fatalf("failed to create SSH signer: %v", err)
	}

	// Register a user and add the SSH key.
	ctx := t.Context()
	userID := "usr" + generateRegistrationToken()
	email := "cmds@example.com"
	pubKeyStr := string(ssh.MarshalAuthorizedKey(sshPubKey))
	fingerprint := strings.TrimPrefix(ssh.FingerprintSHA256(sshPubKey), "SHA256:")
	err = s.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		if _, err := tx.Exec(`INSERT INTO users (user_id, email) VALUES (?, ?)`, userID, email); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO ssh_keys (user_id, public_key, fingerprint) VALUES (?, ?, ?)`, userID, pubKeyStr, fingerprint); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatalf("failed to create user and SSH key: %v", err)
	}

	execNS := "v0@" + s.env.WebHost
	makeToken := func(payloadJSON map[string]any) string {
		payload, err := json.Marshal(payloadJSON)
		if err != nil {
			t.Fatalf("failed to marshal payload: %v", err)
		}
		payloadB64 := base64.RawURLEncoding.EncodeToString(payload)
		sigBlob := createSigBlob(t, sshPrivKey, payload, execNS)
		return "exe0." + payloadB64 + "." + sigBlob
	}

	t.Run("cmds_whoami_only_allows_whoami", func(t *testing.T) {
		token := makeToken(map[string]any{"cmds": []string{"whoami"}})

		req, _ := http.NewRequest("POST", s.httpURL()+"/exec", strings.NewReader("whoami"))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Host = s.env.WebHost

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
		}
	})

	t.Run("cmds_whoami_only_blocks_ls", func(t *testing.T) {
		token := makeToken(map[string]any{"cmds": []string{"whoami"}})

		req, _ := http.NewRequest("POST", s.httpURL()+"/exec", strings.NewReader("ls"))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Host = s.env.WebHost

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusForbidden {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("expected 403, got %d: %s", resp.StatusCode, body)
		}
		body, _ := io.ReadAll(resp.Body)
		if !strings.Contains(string(body), "command not allowed by token permissions") {
			t.Errorf("expected permissions error, got: %s", body)
		}
	})

	t.Run("no_cmds_uses_default_allows_whoami", func(t *testing.T) {
		token := makeToken(map[string]any{})

		req, _ := http.NewRequest("POST", s.httpURL()+"/exec", strings.NewReader("whoami"))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Host = s.env.WebHost

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
		}
	})

	t.Run("empty_cmds_blocks_all", func(t *testing.T) {
		token := makeToken(map[string]any{"cmds": []string{}})

		req, _ := http.NewRequest("POST", s.httpURL()+"/exec", strings.NewReader("whoami"))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Host = s.env.WebHost

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusForbidden {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("expected 403, got %d: %s", resp.StatusCode, body)
		}
	})
}

func TestExecHandlerConsistentErrorMessages(t *testing.T) {
	t.Parallel()

	s := newTestServer(t)

	// Generate keys for a registered user.
	_, privKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}
	sshPubKey, err := ssh.NewPublicKey(privKey.Public().(ed25519.PublicKey))
	if err != nil {
		t.Fatalf("failed to create SSH public key: %v", err)
	}
	sshPrivKey, err := ssh.NewSignerFromKey(privKey)
	if err != nil {
		t.Fatalf("failed to create SSH signer: %v", err)
	}

	// Generate a completely separate key (not registered).
	_, unregisteredPrivKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate unregistered key: %v", err)
	}
	unregisteredSigner, err := ssh.NewSignerFromKey(unregisteredPrivKey)
	if err != nil {
		t.Fatalf("failed to create unregistered SSH signer: %v", err)
	}

	fingerprint := strings.TrimPrefix(ssh.FingerprintSHA256(sshPubKey), "SHA256:")

	// Register user with the first key.
	ctx := t.Context()
	userID := "usr" + generateRegistrationToken()
	err = s.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		if _, err := tx.Exec(`INSERT INTO users (user_id, email) VALUES (?, ?)`, userID, "consistent@example.com"); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO ssh_keys (user_id, public_key, fingerprint) VALUES (?, ?, ?)`,
			userID, string(ssh.MarshalAuthorizedKey(sshPubKey)), fingerprint); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatalf("failed to create user and SSH key: %v", err)
	}

	execRequest := func(token string) (int, string) {
		req, err := http.NewRequest("POST", s.httpURL()+"/exec", strings.NewReader("whoami"))
		if err != nil {
			t.Fatalf("failed to create request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Host = s.env.WebHost

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body)
	}

	execNS := "v0@" + s.env.WebHost

	// Case 1: Fingerprint not found in DB (unregistered key).
	t.Run("fingerprint_not_found", func(t *testing.T) {
		payload := []byte(`{}`)
		payloadB64 := base64.RawURLEncoding.EncodeToString(payload)
		sigBlob := createSigBlob(t, unregisteredSigner, payload, execNS)
		token := "exe0." + payloadB64 + "." + sigBlob

		code, body := execRequest(token)
		if code != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d", code)
		}
		if !strings.Contains(body, "invalid token") {
			t.Errorf("expected 'invalid token' for unregistered fingerprint, got: %s", body)
		}
	})

	// Case 2: Wrong namespace.
	t.Run("wrong_namespace", func(t *testing.T) {
		payload := []byte(`{}`)
		payloadB64 := base64.RawURLEncoding.EncodeToString(payload)
		// Sign with the wrong namespace (anything other than the server's exec namespace).
		sigBlob := createSigBlob(t, sshPrivKey, payload, "v0@evil.com")
		token := "exe0." + payloadB64 + "." + sigBlob

		code, body := execRequest(token)
		if code != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d", code)
		}
		if !strings.Contains(body, "invalid token") {
			t.Errorf("expected 'invalid token' for wrong namespace, got: %s", body)
		}
	})
}

func TestExecHandlerLockedOutUser(t *testing.T) {
	t.Parallel()

	s := newTestServer(t)

	// Generate a test ed25519 key pair.
	pubKey, privKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}
	sshPubKey, err := ssh.NewPublicKey(pubKey)
	if err != nil {
		t.Fatalf("failed to create SSH public key: %v", err)
	}
	sshPrivKey, err := ssh.NewSignerFromKey(privKey)
	if err != nil {
		t.Fatalf("failed to create SSH signer: %v", err)
	}

	// Register a user and add the SSH key.
	ctx := t.Context()
	userID := "usr" + generateRegistrationToken()
	pubKeyStr := string(ssh.MarshalAuthorizedKey(sshPubKey))
	fingerprint := strings.TrimPrefix(ssh.FingerprintSHA256(sshPubKey), "SHA256:")
	err = s.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		if _, err := tx.Exec(`INSERT INTO users (user_id, email) VALUES (?, ?)`, userID, "locked@example.com"); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO ssh_keys (user_id, public_key, fingerprint) VALUES (?, ?, ?)`, userID, pubKeyStr, fingerprint); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatalf("failed to create user and SSH key: %v", err)
	}

	execNS := "v0@" + s.env.WebHost
	payload := []byte(`{}`)
	payloadB64 := base64.RawURLEncoding.EncodeToString(payload)
	sigBlob := createSigBlob(t, sshPrivKey, payload, execNS)
	token := "exe0." + payloadB64 + "." + sigBlob

	// Token should work before lockout.
	t.Run("before_lockout", func(t *testing.T) {
		req, _ := http.NewRequest("POST", s.httpURL()+"/exec", strings.NewReader("whoami"))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Host = s.env.WebHost
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("expected 200 before lockout, got %d: %s", resp.StatusCode, body)
		}
	})

	// Lock out the user.
	err = s.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := tx.Exec(`UPDATE users SET is_locked_out = 1 WHERE user_id = ?`, userID)
		return err
	})
	if err != nil {
		t.Fatalf("failed to lock out user: %v", err)
	}

	// Token should be rejected after lockout.
	t.Run("after_lockout", func(t *testing.T) {
		req, _ := http.NewRequest("POST", s.httpURL()+"/exec", strings.NewReader("whoami"))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Host = s.env.WebHost
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("expected 401 after lockout, got %d: %s", resp.StatusCode, body)
		}
	})
}

func TestValidateVMTokenLockedOutUser(t *testing.T) {
	t.Parallel()

	s := newTestServer(t)

	// Generate a test ed25519 key pair.
	pubKey, privKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}
	sshPubKey, err := ssh.NewPublicKey(pubKey)
	if err != nil {
		t.Fatalf("failed to create SSH public key: %v", err)
	}
	sshPrivKey, err := ssh.NewSignerFromKey(privKey)
	if err != nil {
		t.Fatalf("failed to create SSH signer: %v", err)
	}

	// Register a user and add the SSH key.
	ctx := t.Context()
	userID := "usr" + generateRegistrationToken()
	pubKeyStr := string(ssh.MarshalAuthorizedKey(sshPubKey))
	fingerprint := strings.TrimPrefix(ssh.FingerprintSHA256(sshPubKey), "SHA256:")
	err = s.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		if _, err := tx.Exec(`INSERT INTO users (user_id, email) VALUES (?, ?)`, userID, "vmlocked@example.com"); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO ssh_keys (user_id, public_key, fingerprint) VALUES (?, ?, ?)`, userID, pubKeyStr, fingerprint); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatalf("failed to create user and SSH key: %v", err)
	}

	// Create a VM-namespace token.
	boxName := "testvm"
	namespace := "v0@" + boxName + "." + s.env.BoxHost
	payload := []byte(`{}`)
	payloadB64 := base64.RawURLEncoding.EncodeToString(payload)
	sigBlob := createSigBlob(t, sshPrivKey, payload, namespace)
	token := "exe0." + payloadB64 + "." + sigBlob

	// Token should work before lockout.
	t.Run("before_lockout", func(t *testing.T) {
		result := s.validateVMToken(ctx, token, boxName)
		if result == nil {
			t.Fatal("expected valid result before lockout, got nil")
		}
		if result.UserID != userID {
			t.Errorf("expected userID %q, got %q", userID, result.UserID)
		}
	})

	// Lock out the user.
	err = s.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := tx.Exec(`UPDATE users SET is_locked_out = 1 WHERE user_id = ?`, userID)
		return err
	})
	if err != nil {
		t.Fatalf("failed to lock out user: %v", err)
	}

	// Token should be rejected after lockout.
	t.Run("after_lockout", func(t *testing.T) {
		result := s.validateVMToken(ctx, token, boxName)
		if result != nil {
			t.Fatalf("expected nil result after lockout, got userID=%q", result.UserID)
		}
	})
}

func TestExecHandlerEmptyBearerToken(t *testing.T) {
	t.Parallel()

	s := newTestServer(t)

	t.Run("empty_after_bearer", func(t *testing.T) {
		req, err := http.NewRequest("POST", s.httpURL()+"/exec", strings.NewReader("whoami"))
		if err != nil {
			t.Fatalf("failed to create request: %v", err)
		}
		// "Bearer " with nothing after it.
		req.Header.Set("Authorization", "Bearer ")
		req.Host = s.env.WebHost

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusUnauthorized {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("expected 401 for empty bearer token, got %d: %s", resp.StatusCode, body)
		}
	})

	t.Run("no_auth_header", func(t *testing.T) {
		req, err := http.NewRequest("POST", s.httpURL()+"/exec", strings.NewReader("whoami"))
		if err != nil {
			t.Fatalf("failed to create request: %v", err)
		}
		// No Authorization header at all.
		req.Host = s.env.WebHost

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusUnauthorized {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("expected 401 for missing auth header, got %d: %s", resp.StatusCode, body)
		}
	})

	t.Run("bearer_only_whitespace", func(t *testing.T) {
		req, err := http.NewRequest("POST", s.httpURL()+"/exec", strings.NewReader("whoami"))
		if err != nil {
			t.Fatalf("failed to create request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer    ")
		req.Host = s.env.WebHost

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusUnauthorized {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("expected 401 for whitespace-only bearer token, got %d: %s", resp.StatusCode, body)
		}
	})
}

func TestExecHandlerValidJSON(t *testing.T) {
	t.Parallel()

	s := newTestServer(t)

	// Generate a test ed25519 key pair.
	pubKey, privKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}
	sshPubKey, err := ssh.NewPublicKey(pubKey)
	if err != nil {
		t.Fatalf("failed to create SSH public key: %v", err)
	}
	sshPrivKey, err := ssh.NewSignerFromKey(privKey)
	if err != nil {
		t.Fatalf("failed to create SSH signer: %v", err)
	}

	ctx := t.Context()
	userID := "usr" + generateRegistrationToken()
	email := "json-valid@example.com"
	pubKeyStr := string(ssh.MarshalAuthorizedKey(sshPubKey))
	fingerprint := strings.TrimPrefix(ssh.FingerprintSHA256(sshPubKey), "SHA256:")
	err = s.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		if _, err := tx.Exec(`INSERT INTO users (user_id, email) VALUES (?, ?)`, userID, email); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO ssh_keys (user_id, public_key, fingerprint) VALUES (?, ?, ?)`, userID, pubKeyStr, fingerprint); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatalf("failed to create user and SSH key: %v", err)
	}

	execNS := "v0@" + s.env.WebHost
	payload := []byte(`{}`)
	payloadB64 := base64.RawURLEncoding.EncodeToString(payload)
	sigBlob := createSigBlob(t, sshPrivKey, payload, execNS)
	token := "exe0." + payloadB64 + "." + sigBlob

	exec := func(t *testing.T, cmd string) (int, string) {
		t.Helper()
		req, err := http.NewRequest("POST", s.httpURL()+"/exec", strings.NewReader(cmd))
		if err != nil {
			t.Fatalf("failed to create request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Host = s.env.WebHost
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		ct := resp.Header.Get("Content-Type")
		if ct != "application/json; charset=utf-8" {
			t.Errorf("Content-Type = %q, want application/json; charset=utf-8", ct)
		}
		return resp.StatusCode, string(body)
	}

	assertValidJSON := func(t *testing.T, body string) {
		t.Helper()
		body = strings.TrimSpace(body)
		if !json.Valid([]byte(body)) {
			t.Errorf("response is not valid JSON:\n%s", body)
		}
	}

	// Every DefaultTokenCmds command should return valid JSON.
	t.Run("whoami", func(t *testing.T) {
		status, body := exec(t, "whoami")
		if status != 200 {
			t.Fatalf("status = %d, want 200: %s", status, body)
		}
		assertValidJSON(t, body)
	})

	t.Run("ls", func(t *testing.T) {
		status, body := exec(t, "ls")
		if status != 200 {
			t.Fatalf("status = %d, want 200: %s", status, body)
		}
		assertValidJSON(t, body)
	})

	t.Run("ls_long", func(t *testing.T) {
		status, body := exec(t, "ls -l")
		if status != 200 {
			t.Fatalf("status = %d, want 200: %s", status, body)
		}
		assertValidJSON(t, body)
	})

	t.Run("ssh_key_list", func(t *testing.T) {
		status, body := exec(t, "ssh-key list")
		if status != 200 {
			t.Fatalf("status = %d, want 200: %s", status, body)
		}
		assertValidJSON(t, body)
	})

	// --help on commands should return valid JSON.
	t.Run("ls_help", func(t *testing.T) {
		status, body := exec(t, "ls --help")
		if status != 200 {
			t.Fatalf("status = %d, want 200: %s", status, body)
		}
		assertValidJSON(t, body)
	})

	t.Run("new_help", func(t *testing.T) {
		status, body := exec(t, "new --help")
		if status != 200 {
			t.Fatalf("status = %d, want 200: %s", status, body)
		}
		assertValidJSON(t, body)
	})

	// Error responses should also be valid JSON.
	t.Run("error_unknown_command", func(t *testing.T) {
		status, body := exec(t, "notacommand")
		if status != 404 {
			t.Fatalf("status = %d, want 404: %s", status, body)
		}
		assertValidJSON(t, body)
	})

	t.Run("error_forbidden_command", func(t *testing.T) {
		// rm is not in DefaultTokenCmds
		status, body := exec(t, "rm")
		if status != 403 {
			t.Fatalf("status = %d, want 403: %s", status, body)
		}
		assertValidJSON(t, body)
	})

	// help is in DefaultTokenCmds, so it should work with the default token.
	t.Run("help", func(t *testing.T) {
		status, body := exec(t, "help")
		if status != 200 {
			t.Fatalf("status = %d, want 200: %s", status, body)
		}
		assertValidJSON(t, body)
		if !strings.Contains(body, `"commands"`) {
			t.Errorf("help output should contain commands key, got: %s", body)
		}
	})
}

func TestExecHandlerBodyTooLarge(t *testing.T) {
	t.Parallel()

	s := newTestServer(t)

	// Generate a test key and register it.
	pubKey, privKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}
	sshPubKey, err := ssh.NewPublicKey(pubKey)
	if err != nil {
		t.Fatalf("failed to create SSH public key: %v", err)
	}
	sshPrivKey, err := ssh.NewSignerFromKey(privKey)
	if err != nil {
		t.Fatalf("failed to create SSH signer: %v", err)
	}

	ctx := t.Context()
	userID := "usr" + generateRegistrationToken()
	pubKeyStr := string(ssh.MarshalAuthorizedKey(sshPubKey))
	fingerprint := strings.TrimPrefix(ssh.FingerprintSHA256(sshPubKey), "SHA256:")
	err = s.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		if _, err := tx.Exec(`INSERT INTO users (user_id, email) VALUES (?, ?)`, userID, "body-test@example.com"); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO ssh_keys (user_id, public_key, fingerprint) VALUES (?, ?, ?)`, userID, pubKeyStr, fingerprint); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatalf("failed to create user and SSH key: %v", err)
	}

	execNS := "v0@" + s.env.WebHost
	payload := []byte(`{}`)
	payloadB64 := base64.RawURLEncoding.EncodeToString(payload)
	sigBlob := createSigBlob(t, sshPrivKey, payload, execNS)
	token := "exe0." + payloadB64 + "." + sigBlob

	// Send a body larger than 64KB.
	largeBody := strings.Repeat("a", 65*1024)
	req, err := http.NewRequest("POST", s.httpURL()+"/exec", strings.NewReader(largeBody))
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Host = s.env.WebHost

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 413 for oversized body, got %d: %s", resp.StatusCode, body)
	}
}

func TestExecHandlerRateLimit(t *testing.T) {
	t.Parallel()

	s := newTestServer(t)

	// Generate a test key and register it.
	pubKey, privKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}
	sshPubKey, err := ssh.NewPublicKey(pubKey)
	if err != nil {
		t.Fatalf("failed to create SSH public key: %v", err)
	}
	sshPrivKey, err := ssh.NewSignerFromKey(privKey)
	if err != nil {
		t.Fatalf("failed to create SSH signer: %v", err)
	}
	ctx := t.Context()
	userID := "usr" + generateRegistrationToken()
	pubKeyStr := string(ssh.MarshalAuthorizedKey(sshPubKey))
	fingerprint := strings.TrimPrefix(ssh.FingerprintSHA256(sshPubKey), "SHA256:")
	err = s.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		if _, err := tx.Exec(`INSERT INTO users (user_id, email) VALUES (?, ?)`, userID, "ratelimit@example.com"); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO ssh_keys (user_id, public_key, fingerprint) VALUES (?, ?, ?)`, userID, pubKeyStr, fingerprint); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatalf("failed to create user and SSH key: %v", err)
	}

	execNS := "v0@" + s.env.WebHost
	payload := []byte(`{}`)
	payloadB64 := base64.RawURLEncoding.EncodeToString(payload)
	sigBlob := createSigBlob(t, sshPrivKey, payload, execNS)
	token := "exe0." + payloadB64 + "." + sigBlob

	exec := func() int {
		req, err := http.NewRequest("POST", s.httpURL()+"/exec", strings.NewReader("whoami"))
		if err != nil {
			t.Fatalf("failed to create request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Host = s.env.WebHost
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}

	// Exhaust the rate limit (burst of 60).
	for i := range 60 {
		if status := exec(); status != 200 {
			t.Fatalf("request %d: expected 200, got %d", i, status)
		}
	}

	// Next request should be rate limited.
	if status := exec(); status != 429 {
		t.Fatalf("expected 429 after exhausting rate limit, got %d", status)
	}
}
