package execore

import (
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
	"golang.org/x/crypto/ssh"
)

func TestPeerIntegrationLifecycle(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	userID, _, token := createTestUserWithIntegrationPerms(t, s)

	execURL := s.httpURL() + "/exec"

	execCmd := func(t *testing.T, cmd string) (int, string) {
		t.Helper()
		req, err := http.NewRequest("POST", execURL, strings.NewReader(cmd))
		if err != nil {
			t.Fatalf("NewRequest: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("Do: %v", err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp.StatusCode, string(body)
	}

	// Create a VM so the peer target exists.
	ctx := context.Background()
	err := s.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := tx.Exec(`INSERT INTO boxes (name, status, image, created_by_user_id, ctrhost, region) VALUES ('target-vm', 'running', 'default', ?, 'tcp://local:9080', 'pdx')`, userID)
		return err
	})
	if err != nil {
		t.Fatalf("create box: %v", err)
	}

	// Create an http-proxy integration with --peer.
	code, body := execCmd(t, "integrations add http-proxy --name=mypeer --target=https://target-vm.exe.cloud --peer")
	if code != 200 {
		t.Fatalf("expected 200, got %d: %s", code, body)
	}

	// Verify integration exists in DB as http-proxy type.
	var intCount int
	err = s.db.Rx(ctx, func(ctx context.Context, rx *sqlite.Rx) error {
		return rx.QueryRow(`SELECT COUNT(*) FROM integrations WHERE name = 'mypeer' AND type = 'http-proxy'`).Scan(&intCount)
	})
	if err != nil {
		t.Fatalf("query integration: %v", err)
	}
	if intCount != 1 {
		t.Fatalf("expected 1 integration, got %d", intCount)
	}

	// Verify the config has peer_vm set.
	var configJSON string
	err = s.db.Rx(ctx, func(ctx context.Context, rx *sqlite.Rx) error {
		return rx.QueryRow(`SELECT config FROM integrations WHERE name = 'mypeer'`).Scan(&configJSON)
	})
	if err != nil {
		t.Fatalf("query config: %v", err)
	}
	var cfg httpProxyConfig
	if err := json.Unmarshal([]byte(configJSON), &cfg); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	if cfg.PeerVM != "target-vm" {
		t.Fatalf("expected peer_vm=target-vm, got %q", cfg.PeerVM)
	}
	if !strings.HasPrefix(cfg.Target, "https://target-vm.") {
		t.Fatalf("expected target starting with https://target-vm., got %q", cfg.Target)
	}
	if !strings.HasPrefix(cfg.Header, "Authorization:Bearer exe1.") {
		t.Fatalf("expected Authorization:Bearer exe1.* header, got %q", cfg.Header)
	}

	// Verify the SSH key was created with integration_id.
	var keyIntegrationID *string
	err = s.db.Rx(ctx, func(ctx context.Context, rx *sqlite.Rx) error {
		return rx.QueryRow(`SELECT integration_id FROM ssh_keys WHERE comment = 'peer-mypeer'`).Scan(&keyIntegrationID)
	})
	if err != nil {
		t.Fatalf("query SSH key: %v", err)
	}
	if keyIntegrationID == nil {
		t.Fatal("expected SSH key to have integration_id set")
	}

	// Verify the SSH key has api_key_hint set.
	var apiKeyHint *string
	err = s.db.Rx(ctx, func(ctx context.Context, rx *sqlite.Rx) error {
		return rx.QueryRow(`SELECT api_key_hint FROM ssh_keys WHERE comment = 'peer-mypeer'`).Scan(&apiKeyHint)
	})
	if err != nil {
		t.Fatalf("query api_key_hint: %v", err)
	}
	if apiKeyHint == nil || len(*apiKeyHint) != 4 {
		t.Fatalf("expected 4-char api_key_hint, got %v", apiKeyHint)
	}

	// Try to remove the SSH key directly - should fail.
	code, body = execCmd(t, "ssh-key remove peer-mypeer")
	if code != 422 {
		t.Fatalf("expected 422 for integration-managed key, got %d: %s", code, body)
	}
	if !strings.Contains(body, "managed by an integration") {
		t.Fatalf("expected 'managed by an integration' error, got: %s", body)
	}

	// Remove the integration.
	code, body = execCmd(t, "integrations remove mypeer")
	if code != 200 {
		t.Fatalf("expected 200, got %d: %s", code, body)
	}

	// Verify the SSH key was also removed.
	var keyCount int
	err = s.db.Rx(ctx, func(ctx context.Context, rx *sqlite.Rx) error {
		return rx.QueryRow(`SELECT COUNT(*) FROM ssh_keys WHERE comment = 'peer-mypeer'`).Scan(&keyCount)
	})
	if err != nil {
		t.Fatalf("count SSH keys: %v", err)
	}
	if keyCount != 0 {
		t.Fatalf("expected SSH key to be deleted, found %d", keyCount)
	}
}

func TestPeerIntegrationValidation(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	_, _, token := createTestUserWithIntegrationPerms(t, s)

	execURL := s.httpURL() + "/exec"

	execCmd := func(t *testing.T, cmd string) (int, string) {
		t.Helper()
		req, err := http.NewRequest("POST", execURL, strings.NewReader(cmd))
		if err != nil {
			t.Fatalf("NewRequest: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("Do: %v", err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp.StatusCode, string(body)
	}

	// --peer without --name.
	_, body := execCmd(t, "integrations add http-proxy --target=https://x.exe.cloud --peer")
	if !strings.Contains(body, "--name is required") {
		t.Fatalf("expected --name required error, got: %s", body)
	}

	// --peer without --target.
	_, body = execCmd(t, "integrations add http-proxy --name=bad --peer")
	if !strings.Contains(body, "--target is required") {
		t.Fatalf("expected --target required error, got: %s", body)
	}

	// --peer with a target that isn't a VM.
	_, body = execCmd(t, "integrations add http-proxy --name=bad --target=https://example.com --peer")
	if !strings.Contains(body, "must be a VM") {
		t.Fatalf("expected 'must be a VM' error, got: %s", body)
	}

	// Non-existent VM.
	_, body = execCmd(t, "integrations add http-proxy --name=bad --target=https://nonexistent-vm.exe.cloud --peer")
	if !strings.Contains(body, "not found") {
		t.Fatalf("expected not found error, got: %s", body)
	}

	// --peer with --bearer is mutually exclusive.
	_, body = execCmd(t, "integrations add http-proxy --name=bad --target=https://x.exe.cloud --peer --bearer=bar")
	if !strings.Contains(body, "mutually exclusive") {
		t.Fatalf("expected mutually exclusive error, got: %s", body)
	}
}

func TestPeerIntegrationWithExplicitTarget(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	userID, _, token := createTestUserWithIntegrationPerms(t, s)

	execURL := s.httpURL() + "/exec"

	execCmd := func(t *testing.T, cmd string) (int, string) {
		t.Helper()
		req, err := http.NewRequest("POST", execURL, strings.NewReader(cmd))
		if err != nil {
			t.Fatalf("NewRequest: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("Do: %v", err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp.StatusCode, string(body)
	}

	ctx := context.Background()
	err := s.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := tx.Exec(`INSERT INTO boxes (name, status, image, created_by_user_id, ctrhost, region) VALUES ('backend-vm', 'running', 'default', ?, 'tcp://local:9080', 'pdx')`, userID)
		return err
	})
	if err != nil {
		t.Fatalf("create box: %v", err)
	}

	// Create with --peer and explicit --target.
	code, body := execCmd(t, "integrations add http-proxy --name=explicit --target=https://backend-vm.exe.cloud:8080 --peer")
	if code != 200 {
		t.Fatalf("expected 200, got %d: %s", code, body)
	}

	var configJSON string
	err = s.db.Rx(ctx, func(ctx context.Context, rx *sqlite.Rx) error {
		return rx.QueryRow(`SELECT config FROM integrations WHERE name = 'explicit'`).Scan(&configJSON)
	})
	if err != nil {
		t.Fatalf("query config: %v", err)
	}
	var cfg httpProxyConfig
	if err := json.Unmarshal([]byte(configJSON), &cfg); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	if cfg.Target != "https://backend-vm.exe.cloud:8080" {
		t.Fatalf("expected explicit target, got %q", cfg.Target)
	}
	if cfg.PeerVM != "backend-vm" {
		t.Fatalf("expected peer_vm=backend-vm, got %q", cfg.PeerVM)
	}
}

func createTestUserWithIntegrationPerms(t *testing.T, s *Server) (userID, email, token string) {
	t.Helper()
	ctx := t.Context()

	userID = "usr" + generateRegistrationToken()
	email = userID + "@test.example.com"

	pubKey, privKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	sshPubKey, err := ssh.NewPublicKey(pubKey)
	if err != nil {
		t.Fatalf("NewPublicKey: %v", err)
	}
	sshPrivKey, err := ssh.NewSignerFromKey(privKey)
	if err != nil {
		t.Fatalf("NewSignerFromKey: %v", err)
	}

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
		t.Fatalf("create user: %v", err)
	}

	execNS := "v0@" + s.env.WebHost
	payload := []byte(`{"cmds":["whoami","ls","ssh-key generate-api-key","ssh-key remove","ssh-key list","integrations add","integrations remove","integrations list","integrations attach","integrations detach"]}`)
	payloadB64 := base64.RawURLEncoding.EncodeToString(payload)
	sigBlob := createSigBlob(t, sshPrivKey, payload, execNS)
	token = "exe0." + payloadB64 + "." + sigBlob

	return userID, email, token
}
