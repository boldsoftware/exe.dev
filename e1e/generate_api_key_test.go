package e1e

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"exe.dev/sshkey"
)

// TestGenerateAPIKey tests the ssh-key generate-api-key command end-to-end.
func TestGenerateAPIKey(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, _, keyFile, email := registerForExeDev(t)
	pty.Disconnect()

	execURL := fmt.Sprintf("http://localhost:%d/exec", Env.HTTPPort())

	// execWithToken is a helper for POST /exec with a bearer token.
	execWithToken := func(t *testing.T, token, command string) (*http.Response, []byte) {
		t.Helper()
		req, err := http.NewRequest("POST", execURL, strings.NewReader(command))
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
		return resp, body
	}

	t.Run("create_and_use", func(t *testing.T) {
		out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), keyFile,
			"ssh-key", "generate-api-key", "--json", "--label=e1e-basic")
		if err != nil {
			t.Fatalf("generate-api-key: %v\n%s", err, out)
		}

		var result struct {
			Label       string `json:"label"`
			Token       string `json:"token"`
			Namespace   string `json:"namespace"`
			Fingerprint string `json:"fingerprint"`
		}
		if err := json.Unmarshal(out, &result); err != nil {
			t.Fatalf("parse JSON: %v\n%s", err, out)
		}

		if result.Label != "e1e-basic" {
			t.Errorf("label = %q, want e1e-basic", result.Label)
		}
		if !strings.HasPrefix(result.Token, sshkey.Exe1TokenPrefix) {
			t.Fatalf("token should start with %s, got %q", sshkey.Exe1TokenPrefix, result.Token)
		}
		if !strings.HasPrefix(result.Fingerprint, "SHA256:") {
			t.Errorf("fingerprint = %q, want SHA256:...", result.Fingerprint)
		}

		// Token should work for whoami.
		resp, body := execWithToken(t, result.Token, "whoami --json")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("whoami: %d: %s", resp.StatusCode, body)
		}
		if !strings.Contains(string(body), email) {
			t.Errorf("whoami output should contain %q, got: %s", email, body)
		}
	})

	t.Run("cmds_restriction", func(t *testing.T) {
		out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), keyFile,
			"ssh-key", "generate-api-key", "--json", "--label=e1e-restricted", "--cmds=whoami")
		if err != nil {
			t.Fatalf("generate-api-key: %v\n%s", err, out)
		}
		var result struct {
			Token string `json:"token"`
		}
		json.Unmarshal(out, &result)

		// whoami should work.
		resp, body := execWithToken(t, result.Token, "whoami")
		if resp.StatusCode != http.StatusOK {
			t.Errorf("whoami should succeed, got %d: %s", resp.StatusCode, body)
		}

		// ls should be forbidden.
		resp, body = execWithToken(t, result.Token, "ls")
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("ls should be 403, got %d: %s", resp.StatusCode, body)
		}
	})

	t.Run("cannot_create_tokens", func(t *testing.T) {
		// Generate a token with default permissions.
		out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), keyFile,
			"ssh-key", "generate-api-key", "--json", "--label=e1e-no-escalate")
		if err != nil {
			t.Fatalf("generate-api-key: %v\n%s", err, out)
		}
		var result struct {
			Token string `json:"token"`
		}
		json.Unmarshal(out, &result)

		// The token should NOT be able to run generate-api-key.
		resp, body := execWithToken(t, result.Token, "ssh-key generate-api-key --json --label=child")
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("generate-api-key should be 403, got %d: %s", resp.StatusCode, body)
		}
	})

	t.Run("duplicate_label", func(t *testing.T) {
		out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), keyFile,
			"ssh-key", "generate-api-key", "--json", "--label=e1e-dupe")
		if err != nil {
			t.Fatalf("first create: %v\n%s", err, out)
		}

		// Same label again should fail.
		out, err = Env.servers.RunExeDevSSHCommand(Env.context(t), keyFile,
			"ssh-key", "generate-api-key", "--json", "--label=e1e-dupe")
		if err == nil {
			t.Fatalf("duplicate label should fail, got: %s", out)
		}
		if !strings.Contains(string(out), "already exists") {
			t.Errorf("expected 'already exists' error, got: %s", out)
		}
	})

	t.Run("visible_in_list", func(t *testing.T) {
		out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), keyFile,
			"ssh-key", "generate-api-key", "--json", "--label=e1e-list-check")
		if err != nil {
			t.Fatalf("generate-api-key: %v\n%s", err, out)
		}

		listOut := runParseExeDevJSON[sshKeyListOutput](t, keyFile, "ssh-key", "list", "--json")
		found := false
		for _, key := range listOut.SSHKeys {
			if key.Name == "e1e-list-check" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected 'e1e-list-check' in ssh-key list")
		}
	})

	t.Run("revoke_and_verify", func(t *testing.T) {
		// Create a token.
		out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), keyFile,
			"ssh-key", "generate-api-key", "--json", "--label=e1e-revoke")
		if err != nil {
			t.Fatalf("generate-api-key: %v\n%s", err, out)
		}
		var result struct {
			Token string `json:"token"`
		}
		json.Unmarshal(out, &result)

		// Token works.
		resp, body := execWithToken(t, result.Token, "whoami")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("whoami before revoke: %d: %s", resp.StatusCode, body)
		}

		// Revoke via ssh-key remove.
		out, err = Env.servers.RunExeDevSSHCommand(Env.context(t), keyFile,
			"ssh-key", "remove", "e1e-revoke")
		if err != nil {
			t.Fatalf("ssh-key remove: %v\n%s", err, out)
		}

		// Token should now be rejected.
		resp, _ = execWithToken(t, result.Token, "whoami")
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("revoked token should be 401, got %d", resp.StatusCode)
		}
	})

	t.Run("with_expiry", func(t *testing.T) {
		out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), keyFile,
			"ssh-key", "generate-api-key", "--json", "--label=e1e-expiry", "--exp=90d")
		if err != nil {
			t.Fatalf("generate-api-key: %v\n%s", err, out)
		}
		var result struct {
			Token     string `json:"token"`
			ExpiresAt string `json:"expires_at"`
		}
		json.Unmarshal(out, &result)

		if result.ExpiresAt == "" {
			t.Error("expected expires_at to be set")
		}

		// Token should work (it hasn't expired yet).
		resp, body := execWithToken(t, result.Token, "whoami")
		if resp.StatusCode != http.StatusOK {
			t.Errorf("whoami with expiry: %d: %s", resp.StatusCode, body)
		}
	})
}
