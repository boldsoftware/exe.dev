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

	"exe.dev/exemenu"
	"exe.dev/sqlite"
	"exe.dev/sshkey"
	"golang.org/x/crypto/ssh"
)

// createTestUserWithSSHKeyAddPerm creates a test user whose token allows ssh-key add.
func createTestUserWithSSHKeyAddPerm(t *testing.T, s *Server) (userID, email, token string) {
	t.Helper()
	ctx := t.Context()
	userID = "usr" + generateRegistrationToken()
	email = userID + "@test.example.com"

	pubKey, privKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sshPubKey, _ := ssh.NewPublicKey(pubKey)
	sshPrivKey, _ := ssh.NewSignerFromKey(privKey)
	pubKeyStr := string(ssh.MarshalAuthorizedKey(sshPubKey))
	fingerprint := strings.TrimPrefix(ssh.FingerprintSHA256(sshPubKey), "SHA256:")

	err = s.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		if _, err := tx.Exec(`INSERT INTO users (user_id, email, root_support) VALUES (?, ?, 1)`, userID, email); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO ssh_keys (user_id, public_key, fingerprint) VALUES (?, ?, ?)`, userID, pubKeyStr, fingerprint); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	payload := []byte(`{"cmds":["ssh-key add","ssh-key list","whoami"]}`)
	payloadB64 := base64.RawURLEncoding.EncodeToString(payload)
	sigBlob := createSigBlob(t, sshPrivKey, payload, "v0@"+s.env.WebHost)
	token = sshkey.TokenPrefix + payloadB64 + "." + sigBlob
	return userID, email, token
}

// TestSSHKeyAddPermissions tests the ssh-key add command with --cmds, --vm, --exp flags.
func TestSSHKeyAddPermissions(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	_, _, callerToken := createTestUserWithSSHKeyAddPerm(t, s)

	t.Run("add_with_cmds", func(t *testing.T) {
		pk := generateTestSSHKey(t)
		cmd := "ssh-key add --json --cmds=ls,whoami " + pk
		resp, body := execWithBearer(t, s, callerToken, cmd)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
		}
		var result map[string]any
		if err := json.Unmarshal([]byte(body), &result); err != nil {
			t.Fatalf("Decode: %v", err)
		}
		if result["status"] != "added" {
			t.Errorf("status = %v, want added", result["status"])
		}
		permsRaw, ok := result["permissions"]
		if !ok {
			t.Fatal("expected permissions in JSON output")
		}
		permsMap, ok := permsRaw.(map[string]any)
		if !ok {
			t.Fatalf("expected permissions to be object, got %T", permsRaw)
		}
		cmds, ok := permsMap["cmds"].([]any)
		if !ok || len(cmds) != 2 {
			t.Errorf("expected cmds=[ls,whoami], got %v", permsMap["cmds"])
		}
	})

	t.Run("add_with_exp", func(t *testing.T) {
		pk := generateTestSSHKey(t)
		cmd := "ssh-key add --json --exp=30d " + pk
		resp, body := execWithBearer(t, s, callerToken, cmd)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
		}
		var result map[string]any
		if err := json.Unmarshal([]byte(body), &result); err != nil {
			t.Fatalf("Decode: %v", err)
		}
		permsRaw, ok := result["permissions"]
		if !ok {
			t.Fatal("expected permissions in JSON output")
		}
		permsMap := permsRaw.(map[string]any)
		if _, ok := permsMap["exp"]; !ok {
			t.Error("expected exp in permissions")
		}
	})

	t.Run("add_without_restrictions", func(t *testing.T) {
		pk := generateTestSSHKey(t)
		cmd := "ssh-key add --json " + pk
		resp, body := execWithBearer(t, s, callerToken, cmd)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
		}
		var result map[string]any
		if err := json.Unmarshal([]byte(body), &result); err != nil {
			t.Fatalf("Decode: %v", err)
		}
		if result["status"] != "added" {
			t.Errorf("status = %v, want added", result["status"])
		}
		if _, ok := result["permissions"]; ok {
			t.Error("unrestricted key should not have permissions in output")
		}
	})

	t.Run("non_sudoer_denied_perms", func(t *testing.T) {
		// Create a non-sudoer user.
		ctx := t.Context()
		userID := "usr" + generateRegistrationToken()
		email := userID + "@test.example.com"

		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		sshPub, _ := ssh.NewPublicKey(pub)
		sshPriv, _ := ssh.NewSignerFromKey(priv)
		pubStr := string(ssh.MarshalAuthorizedKey(sshPub))
		fp := strings.TrimPrefix(ssh.FingerprintSHA256(sshPub), "SHA256:")

		err = s.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
			if _, err := tx.Exec(`INSERT INTO users (user_id, email) VALUES (?, ?)`, userID, email); err != nil {
				return err
			}
			if _, err := tx.Exec(`INSERT INTO ssh_keys (user_id, public_key, fingerprint) VALUES (?, ?, ?)`, userID, pubStr, fp); err != nil {
				return err
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}

		payload := []byte(`{"cmds":["ssh-key add","ssh-key list","whoami"]}`)
		payloadB64 := base64.RawURLEncoding.EncodeToString(payload)
		sigBlob := createSigBlob(t, sshPriv, payload, "v0@"+s.env.WebHost)
		token := sshkey.TokenPrefix + payloadB64 + "." + sigBlob

		// Non-sudoer can add a key without restrictions.
		pk := generateTestSSHKey(t)
		resp, body := execWithBearer(t, s, token, "ssh-key add --json "+pk)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 for unrestricted add, got %d: %s", resp.StatusCode, body)
		}

		// Non-sudoer denied when using --cmds.
		pk2 := generateTestSSHKey(t)
		resp, body = execWithBearer(t, s, token, "ssh-key add --json --cmds=whoami "+pk2)
		if resp.StatusCode == http.StatusOK {
			t.Fatalf("non-sudoer should be denied --cmds, got 200: %s", body)
		}
		if !strings.Contains(body, "root support privileges") {
			t.Errorf("expected root support error, got: %s", body)
		}

		// Non-sudoer denied when using --exp.
		pk3 := generateTestSSHKey(t)
		resp, body = execWithBearer(t, s, token, "ssh-key add --json --exp=30d "+pk3)
		if resp.StatusCode == http.StatusOK {
			t.Fatalf("non-sudoer should be denied --exp, got 200: %s", body)
		}
		if !strings.Contains(body, "root support privileges") {
			t.Errorf("expected root support error, got: %s", body)
		}
	})
}

// TestSSHKeyPermsEnforcement tests that SSH key permissions are enforced.
func TestSSHKeyPermsEnforcement(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	ctx := t.Context()

	userID := "usr" + generateRegistrationToken()
	email := userID + "@test.example.com"

	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sshPub, _ := ssh.NewPublicKey(pub)
	pubStr := string(ssh.MarshalAuthorizedKey(sshPub))
	fp := strings.TrimPrefix(ssh.FingerprintSHA256(sshPub), "SHA256:")

	err = s.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		if _, err := tx.Exec(`INSERT INTO users (user_id, email) VALUES (?, ?)`, userID, email); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO ssh_keys (user_id, public_key, fingerprint, permissions) VALUES (?, ?, ?, ?)`,
			userID, pubStr, fp, `{"cmds":["whoami","ls"]}`); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Run("perms_lookup", func(t *testing.T) {
		perms, err := s.getSSHKeyPermsByPublicKey(ctx, pubStr)
		if err != nil {
			t.Fatalf("getSSHKeyPermsByPublicKey: %v", err)
		}
		if perms == nil {
			t.Fatal("expected non-nil permissions")
		}
		if !perms.AllowsCommand("whoami") {
			t.Error("should allow whoami")
		}
		if !perms.AllowsCommand("ls") {
			t.Error("should allow ls")
		}
		if perms.AllowsCommand("rm") {
			t.Error("should not allow rm")
		}
		if perms.IsExpired() {
			t.Error("should not be expired")
		}
	})

	t.Run("expired_key_perms", func(t *testing.T) {
		pub2, _, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		sshPub2, _ := ssh.NewPublicKey(pub2)
		pubStr2 := string(ssh.MarshalAuthorizedKey(sshPub2))
		fp2 := strings.TrimPrefix(ssh.FingerprintSHA256(sshPub2), "SHA256:")

		err = s.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
			_, err := tx.Exec(`INSERT INTO ssh_keys (user_id, public_key, fingerprint, permissions) VALUES (?, ?, ?, ?)`,
				userID, pubStr2, fp2, `{"exp":1000000000}`)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}

		perms, err := s.getSSHKeyPermsByPublicKey(ctx, pubStr2)
		if err != nil {
			t.Fatalf("getSSHKeyPermsByPublicKey: %v", err)
		}
		if perms == nil {
			t.Fatal("expected non-nil permissions")
		}
		if !perms.IsExpired() {
			t.Error("should be expired")
		}
	})
}

// TestSSHKeyPermsEnforceViaSSH tests that SSH key permissions are enforced
// in the executeCommandWithLogging path (the path used by SSH exec and REPL).
func TestSSHKeyPermsEnforceViaSSH(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	ctx := t.Context()

	userID := "usr" + generateRegistrationToken()
	email := userID + "@test.example.com"

	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sshPub, _ := ssh.NewPublicKey(pub)
	pubStr := string(ssh.MarshalAuthorizedKey(sshPub))
	fp := strings.TrimPrefix(ssh.FingerprintSHA256(sshPub), "SHA256:")

	err = s.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		if _, err := tx.Exec(`INSERT INTO users (user_id, email) VALUES (?, ?)`, userID, email); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO ssh_keys (user_id, public_key, fingerprint, permissions) VALUES (?, ?, ?, ?)`,
			userID, pubStr, fp, `{"cmds":["whoami","help"]}`); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	ss := NewSSHServer(s)
	var buf strings.Builder
	cc := &exemenu.CommandContext{
		User:      &exemenu.UserInfo{ID: userID, Email: email},
		PublicKey: pubStr,
		Output:    &buf,
		Logger:    s.slog(),
	}

	t.Run("allowed", func(t *testing.T) {
		buf.Reset()
		rc := ss.executeCommandWithLogging(ctx, cc, []string{"whoami"})
		if rc != 0 {
			t.Errorf("expected rc=0 for allowed command, got %d: %s", rc, buf.String())
		}
	})

	t.Run("denied", func(t *testing.T) {
		buf.Reset()
		rc := ss.executeCommandWithLogging(ctx, cc, []string{"ls"})
		if rc != 1 {
			t.Errorf("expected rc=1 for denied command, got %d: %s", rc, buf.String())
		}
		if !strings.Contains(buf.String(), "command not allowed by SSH key permissions") {
			t.Errorf("expected permission denied error, got: %s", buf.String())
		}
	})

	t.Run("expired", func(t *testing.T) {
		pub2, _, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		sshPub2, _ := ssh.NewPublicKey(pub2)
		pubStr2 := string(ssh.MarshalAuthorizedKey(sshPub2))
		fp2 := strings.TrimPrefix(ssh.FingerprintSHA256(sshPub2), "SHA256:")

		err = s.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
			_, err := tx.Exec(`INSERT INTO ssh_keys (user_id, public_key, fingerprint, permissions) VALUES (?, ?, ?, ?)`,
				userID, pubStr2, fp2, `{"exp":1000000000}`)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}

		buf.Reset()
		cc2 := &exemenu.CommandContext{
			User:      &exemenu.UserInfo{ID: userID, Email: email},
			PublicKey: pubStr2,
			Output:    &buf,
			Logger:    s.slog(),
		}
		rc := ss.executeCommandWithLogging(ctx, cc2, []string{"whoami"})
		if rc != 1 {
			t.Errorf("expected rc=1 for expired key, got %d: %s", rc, buf.String())
		}
		if !strings.Contains(buf.String(), "SSH key has expired") {
			t.Errorf("expected expired error, got: %s", buf.String())
		}
	})

	t.Run("unrestricted", func(t *testing.T) {
		pub3, _, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		sshPub3, _ := ssh.NewPublicKey(pub3)
		pubStr3 := string(ssh.MarshalAuthorizedKey(sshPub3))
		fp3 := strings.TrimPrefix(ssh.FingerprintSHA256(sshPub3), "SHA256:")

		err = s.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
			_, err := tx.Exec(`INSERT INTO ssh_keys (user_id, public_key, fingerprint) VALUES (?, ?, ?)`,
				userID, pubStr3, fp3)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}

		buf.Reset()
		cc3 := &exemenu.CommandContext{
			User:      &exemenu.UserInfo{ID: userID, Email: email},
			PublicKey: pubStr3,
			Output:    &buf,
			Logger:    s.slog(),
		}
		rc := ss.executeCommandWithLogging(ctx, cc3, []string{"whoami"})
		if rc != 0 {
			t.Errorf("expected rc=0 for unrestricted key, got %d: %s", rc, buf.String())
		}
	})
}

// generateTestSSHKey generates a new ed25519 SSH key and returns the authorized_keys format string.
func generateTestSSHKey(t *testing.T) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub)))
}

// execWithBearer sends a POST /exec request with a bearer token.
func execWithBearer(t *testing.T, s *Server, token, cmd string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest("POST", s.httpURL()+"/exec", strings.NewReader(cmd))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Host = s.env.WebHost
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp, string(body)
}
