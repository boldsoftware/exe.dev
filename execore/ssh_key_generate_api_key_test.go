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
	"time"

	"exe.dev/sqlite"
	"exe.dev/sshkey"
	"golang.org/x/crypto/ssh"
)

func TestSSHKeyGenerateAPIKey(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	_, _, token := createTestUserWithGenerateAPIKeyPerm(t, s)

	t.Run("basic", func(t *testing.T) {
		req, err := http.NewRequest("POST", s.httpURL()+"/exec", strings.NewReader("ssh-key generate-api-key --label=test-key"))
		if err != nil {
			t.Fatalf("NewRequest: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Host = s.env.WebHost

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("Do: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
		}

		var result map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			t.Fatalf("Decode: %v", err)
		}

		if result["label"] != "test-key" {
			t.Errorf("label = %v, want test-key", result["label"])
		}
		fp, ok := result["fingerprint"].(string)
		if !ok || !strings.HasPrefix(fp, "SHA256:") {
			t.Errorf("fingerprint = %v, want SHA256:...", result["fingerprint"])
		}
		tokenStr, ok := result["token"].(string)
		if !ok || !strings.HasPrefix(tokenStr, sshkey.Exe1TokenPrefix) {
			t.Fatalf("expected exe1 token, got %v", result["token"])
		}

		// The generated token should work for whoami.
		req2, _ := http.NewRequest("POST", s.httpURL()+"/exec", strings.NewReader("whoami"))
		req2.Header.Set("Authorization", "Bearer "+tokenStr)
		req2.Host = s.env.WebHost
		resp2, err := http.DefaultClient.Do(req2)
		if err != nil {
			t.Fatalf("Do whoami: %v", err)
		}
		defer resp2.Body.Close()
		if resp2.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp2.Body)
			t.Fatalf("whoami: expected 200, got %d: %s", resp2.StatusCode, body)
		}
	})

	t.Run("with_cmds", func(t *testing.T) {
		req, _ := http.NewRequest("POST", s.httpURL()+"/exec", strings.NewReader("ssh-key generate-api-key --label=restricted --cmds=whoami,ls"))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Host = s.env.WebHost

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("Do: %v", err)
		}
		defer resp.Body.Close()

		var result map[string]any
		json.NewDecoder(resp.Body).Decode(&result)
		tokenStr := result["token"].(string)

		// whoami should work.
		req2, _ := http.NewRequest("POST", s.httpURL()+"/exec", strings.NewReader("whoami"))
		req2.Header.Set("Authorization", "Bearer "+tokenStr)
		req2.Host = s.env.WebHost
		resp2, err := http.DefaultClient.Do(req2)
		if err != nil {
			t.Fatalf("Do whoami: %v", err)
		}
		defer resp2.Body.Close()
		if resp2.StatusCode != http.StatusOK {
			t.Errorf("whoami should work, got %d", resp2.StatusCode)
		}

		// rm should be blocked.
		req3, _ := http.NewRequest("POST", s.httpURL()+"/exec", strings.NewReader("rm nonexistent"))
		req3.Header.Set("Authorization", "Bearer "+tokenStr)
		req3.Host = s.env.WebHost
		resp3, err := http.DefaultClient.Do(req3)
		if err != nil {
			t.Fatalf("Do rm: %v", err)
		}
		defer resp3.Body.Close()
		if resp3.StatusCode != http.StatusForbidden {
			t.Errorf("rm should be forbidden, got %d", resp3.StatusCode)
		}
	})

	t.Run("with_exp", func(t *testing.T) {
		req, _ := http.NewRequest("POST", s.httpURL()+"/exec", strings.NewReader("ssh-key generate-api-key --label=expiring --exp=30d"))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Host = s.env.WebHost

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("Do: %v", err)
		}
		defer resp.Body.Close()

		var result map[string]any
		json.NewDecoder(resp.Body).Decode(&result)

		if result["expires_at"] == nil {
			t.Error("expected expires_at to be set")
		}
	})

	t.Run("default_label", func(t *testing.T) {
		req, _ := http.NewRequest("POST", s.httpURL()+"/exec", strings.NewReader("ssh-key generate-api-key"))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Host = s.env.WebHost
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("Do: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
		}
		var result map[string]any
		json.NewDecoder(resp.Body).Decode(&result)
		if result["label"] != "api-key" {
			t.Errorf("default label = %v, want api-key", result["label"])
		}
	})

	t.Run("token_cannot_create_tokens", func(t *testing.T) {
		// Create a token with default permissions.
		req, _ := http.NewRequest("POST", s.httpURL()+"/exec", strings.NewReader("ssh-key generate-api-key --label=limited"))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Host = s.env.WebHost
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("Do: %v", err)
		}
		var result map[string]any
		json.NewDecoder(resp.Body).Decode(&result)
		resp.Body.Close()
		childToken := result["token"].(string)

		// The child token should NOT be able to generate more tokens.
		req2, _ := http.NewRequest("POST", s.httpURL()+"/exec", strings.NewReader("ssh-key generate-api-key --label=grandchild"))
		req2.Header.Set("Authorization", "Bearer "+childToken)
		req2.Host = s.env.WebHost
		resp2, err := http.DefaultClient.Do(req2)
		if err != nil {
			t.Fatalf("Do: %v", err)
		}
		resp2.Body.Close()
		if resp2.StatusCode != http.StatusForbidden {
			t.Errorf("child token creating tokens: expected 403, got %d", resp2.StatusCode)
		}
	})

	t.Run("visible_in_ssh_key_list", func(t *testing.T) {
		req, _ := http.NewRequest("POST", s.httpURL()+"/exec", strings.NewReader("ssh-key generate-api-key --label=list-check"))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Host = s.env.WebHost
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("Do create: %v", err)
		}
		resp.Body.Close()

		// List keys and verify our label appears.
		req2, _ := http.NewRequest("POST", s.httpURL()+"/exec", strings.NewReader("ssh-key list"))
		req2.Header.Set("Authorization", "Bearer "+token)
		req2.Host = s.env.WebHost
		resp2, err := http.DefaultClient.Do(req2)
		if err != nil {
			t.Fatalf("Do list: %v", err)
		}
		defer resp2.Body.Close()
		var listResult map[string]any
		json.NewDecoder(resp2.Body).Decode(&listResult)
		keys, ok := listResult["ssh_keys"].([]any)
		if !ok {
			t.Fatalf("ssh_keys not an array: %v", listResult)
		}
		found := false
		for _, k := range keys {
			km, _ := k.(map[string]any)
			if km["name"] == "list-check" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected 'list-check' key in ssh-key list, got: %v", keys)
		}
	})

	t.Run("duplicate_label", func(t *testing.T) {
		// Create first token with a label.
		req, _ := http.NewRequest("POST", s.httpURL()+"/exec", strings.NewReader("ssh-key generate-api-key --label=dupe-test"))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Host = s.env.WebHost
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("Do: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("first create: expected 200, got %d", resp.StatusCode)
		}

		// Second token with same label should fail.
		req2, _ := http.NewRequest("POST", s.httpURL()+"/exec", strings.NewReader("ssh-key generate-api-key --label=dupe-test"))
		req2.Header.Set("Authorization", "Bearer "+token)
		req2.Host = s.env.WebHost
		resp2, err := http.DefaultClient.Do(req2)
		if err != nil {
			t.Fatalf("Do: %v", err)
		}
		defer resp2.Body.Close()
		if resp2.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("duplicate label: expected 422, got %d", resp2.StatusCode)
		}
		body, _ := io.ReadAll(resp2.Body)
		if !strings.Contains(string(body), "already exists") {
			t.Errorf("expected 'already exists' in error, got: %s", body)
		}
	})

	t.Run("revoke_via_ssh_key_remove", func(t *testing.T) {
		// Create a token.
		req, _ := http.NewRequest("POST", s.httpURL()+"/exec", strings.NewReader("ssh-key generate-api-key --label=to-revoke"))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Host = s.env.WebHost
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("Do create: %v", err)
		}
		var createResult map[string]any
		json.NewDecoder(resp.Body).Decode(&createResult)
		resp.Body.Close()
		newToken := createResult["token"].(string)

		// Verify the token works.
		req2, _ := http.NewRequest("POST", s.httpURL()+"/exec", strings.NewReader("whoami"))
		req2.Header.Set("Authorization", "Bearer "+newToken)
		req2.Host = s.env.WebHost
		resp2, err := http.DefaultClient.Do(req2)
		if err != nil {
			t.Fatalf("Do whoami: %v", err)
		}
		resp2.Body.Close()
		if resp2.StatusCode != http.StatusOK {
			t.Fatalf("token should work before revocation, got %d", resp2.StatusCode)
		}

		// Revoke by removing the SSH key by label.
		req3, _ := http.NewRequest("POST", s.httpURL()+"/exec", strings.NewReader("ssh-key remove to-revoke"))
		req3.Header.Set("Authorization", "Bearer "+token)
		req3.Host = s.env.WebHost
		resp3, err := http.DefaultClient.Do(req3)
		if err != nil {
			t.Fatalf("Do remove: %v", err)
		}
		resp3.Body.Close()
		if resp3.StatusCode != http.StatusOK {
			t.Fatalf("ssh-key remove should succeed, got %d", resp3.StatusCode)
		}

		// Token should no longer work.
		req4, _ := http.NewRequest("POST", s.httpURL()+"/exec", strings.NewReader("whoami"))
		req4.Header.Set("Authorization", "Bearer "+newToken)
		req4.Host = s.env.WebHost
		resp4, err := http.DefaultClient.Do(req4)
		if err != nil {
			t.Fatalf("Do whoami after revoke: %v", err)
		}
		resp4.Body.Close()
		if resp4.StatusCode != http.StatusUnauthorized {
			t.Errorf("revoked token should return 401, got %d", resp4.StatusCode)
		}
	})
}

func TestParseDuration(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input   string
		want    time.Duration
		wantErr bool
	}{
		{"30d", 30 * 24 * time.Hour, false},
		{"1y", 365 * 24 * time.Hour, false},
		{"2w", 14 * 24 * time.Hour, false},
		{"24h", 24 * time.Hour, false},
		{"3m", 90 * 24 * time.Hour, false},
		{"never", 0, false},
		{"", 0, false},
		{"0d", 0, true},
		{"-1d", 0, true},
		{"abc", 0, true},
		{"30x", 0, true},
	}
	for _, tt := range tests {
		got, err := parseDuration(tt.input)
		if (err != nil) != tt.wantErr {
			t.Errorf("parseDuration(%q) error=%v, wantErr=%v", tt.input, err, tt.wantErr)
			continue
		}
		if !tt.wantErr && got != tt.want {
			t.Errorf("parseDuration(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func createTestUserWithGenerateAPIKeyPerm(t *testing.T, s *Server) (userID, email, token string) {
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
	payload := []byte(`{"cmds":["whoami","ls","ssh-key generate-api-key","ssh-key remove","ssh-key list"]}`)
	payloadB64 := base64.RawURLEncoding.EncodeToString(payload)
	sigBlob := createSigBlob(t, sshPrivKey, payload, execNS)
	token = "exe0." + payloadB64 + "." + sigBlob

	return userID, email, token
}
