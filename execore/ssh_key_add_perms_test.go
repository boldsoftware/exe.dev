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

// TestSSHKeyAddWithTag tests the ssh-key add command with --tag flag.
func TestSSHKeyAddWithTag(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	_, _, callerToken := createTestUserWithSSHKeyAddPerm(t, s)

	t.Run("add_with_tag", func(t *testing.T) {
		pk := generateTestSSHKey(t)
		cmd := "ssh-key add --json --tag=prod " + pk
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
		permsMap := permsRaw.(map[string]any)
		if permsMap["tag"] != "prod" {
			t.Errorf("expected tag=prod, got %v", permsMap["tag"])
		}
	})

	t.Run("add_with_invalid_tag", func(t *testing.T) {
		pk := generateTestSSHKey(t)
		cmd := "ssh-key add --json --tag=INVALID " + pk
		resp, body := execWithBearer(t, s, callerToken, cmd)
		if resp.StatusCode == http.StatusOK {
			t.Fatalf("expected error for invalid tag, got 200: %s", body)
		}
		if !strings.Contains(body, "invalid tag name") {
			t.Errorf("expected invalid tag error, got: %s", body)
		}
	})

	t.Run("vm_and_tag_mutually_exclusive", func(t *testing.T) {
		pk := generateTestSSHKey(t)
		cmd := "ssh-key add --json --vm=test --tag=prod " + pk
		resp, body := execWithBearer(t, s, callerToken, cmd)
		if resp.StatusCode == http.StatusOK {
			t.Fatalf("expected error for --vm + --tag, got 200: %s", body)
		}
		if !strings.Contains(body, "mutually exclusive") {
			t.Errorf("expected mutually exclusive error, got: %s", body)
		}
	})

	t.Run("non_sudoer_denied_tag", func(t *testing.T) {
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

		pk := generateTestSSHKey(t)
		resp, body := execWithBearer(t, s, token, "ssh-key add --json --tag=prod "+pk)
		if resp.StatusCode == http.StatusOK {
			t.Fatalf("non-sudoer should be denied --tag, got 200: %s", body)
		}
		if !strings.Contains(body, "root support privileges") {
			t.Errorf("expected root support error, got: %s", body)
		}
	})
}

// TestSSHKeyPermsTagScope tests the SSHKeyPerms tag-scoping methods.
func TestSSHKeyPermsTagScope(t *testing.T) {
	t.Parallel()

	t.Run("AllowsBoxByTag_nil_perms", func(t *testing.T) {
		var p *SSHKeyPerms
		if !p.AllowsBoxByTag(nil) {
			t.Error("nil perms should allow all")
		}
		if !p.AllowsBoxByTag([]string{"prod"}) {
			t.Error("nil perms should allow all")
		}
	})

	t.Run("AllowsBoxByTag_no_tag_restriction", func(t *testing.T) {
		p := &SSHKeyPerms{Cmds: []string{"ls"}}
		if !p.AllowsBoxByTag(nil) {
			t.Error("no tag restriction should allow all")
		}
		if !p.AllowsBoxByTag([]string{"prod"}) {
			t.Error("no tag restriction should allow all")
		}
	})

	t.Run("AllowsBoxByTag_tag_present", func(t *testing.T) {
		p := &SSHKeyPerms{Tag: "prod"}
		if !p.AllowsBoxByTag([]string{"staging", "prod"}) {
			t.Error("tag 'prod' should match")
		}
	})

	t.Run("AllowsBoxByTag_tag_missing", func(t *testing.T) {
		p := &SSHKeyPerms{Tag: "prod"}
		if p.AllowsBoxByTag([]string{"staging"}) {
			t.Error("tag 'prod' should not match [staging]")
		}
		if p.AllowsBoxByTag(nil) {
			t.Error("tag 'prod' should not match empty tags")
		}
	})

	t.Run("RequiredTag", func(t *testing.T) {
		var p *SSHKeyPerms
		if p.RequiredTag() != "" {
			t.Error("nil perms should return empty")
		}
		p2 := &SSHKeyPerms{Tag: "staging"}
		if p2.RequiredTag() != "staging" {
			t.Error("expected 'staging'")
		}
	})

	t.Run("parseSSHKeyPerms_with_tag", func(t *testing.T) {
		p, err := parseSSHKeyPerms(`{"tag":"ci"}`)
		if err != nil {
			t.Fatal(err)
		}
		if p == nil {
			t.Fatal("expected non-nil")
		}
		if p.Tag != "ci" {
			t.Errorf("tag = %q, want ci", p.Tag)
		}
	})

	t.Run("parseSSHKeyPerms_only_tag_not_nil", func(t *testing.T) {
		p, err := parseSSHKeyPerms(`{"tag":"prod"}`)
		if err != nil {
			t.Fatal(err)
		}
		if p == nil {
			t.Fatal("expected non-nil perms when tag is set")
		}
	})
}

// TestInheritCallerRestrictions tests the restriction inheritance logic.
func TestInheritCallerRestrictions(t *testing.T) {
	t.Parallel()

	t.Run("nil_caller_noop", func(t *testing.T) {
		newPerms := map[string]any{}
		if err := inheritCallerRestrictions(nil, newPerms); err != nil {
			t.Fatal(err)
		}
		if len(newPerms) != 0 {
			t.Errorf("expected empty, got %v", newPerms)
		}
	})

	t.Run("tag_inherited", func(t *testing.T) {
		caller := &SSHKeyPerms{Tag: "ci"}
		newPerms := map[string]any{}
		if err := inheritCallerRestrictions(caller, newPerms); err != nil {
			t.Fatal(err)
		}
		if newPerms["tag"] != "ci" {
			t.Errorf("expected tag=ci, got %v", newPerms["tag"])
		}
	})

	t.Run("tag_same_ok", func(t *testing.T) {
		caller := &SSHKeyPerms{Tag: "ci"}
		newPerms := map[string]any{"tag": "ci"}
		if err := inheritCallerRestrictions(caller, newPerms); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("tag_different_rejected", func(t *testing.T) {
		caller := &SSHKeyPerms{Tag: "ci"}
		newPerms := map[string]any{"tag": "deploy"}
		err := inheritCallerRestrictions(caller, newPerms)
		if err == nil {
			t.Fatal("expected error for different tag")
		}
		if !strings.Contains(err.Error(), "scoped to tag") {
			t.Errorf("expected scoped-to-tag error, got: %v", err)
		}
	})

	t.Run("vm_inherited", func(t *testing.T) {
		caller := &SSHKeyPerms{VM: "my-vm"}
		newPerms := map[string]any{}
		if err := inheritCallerRestrictions(caller, newPerms); err != nil {
			t.Fatal(err)
		}
		if newPerms["vm"] != "my-vm" {
			t.Errorf("expected vm=my-vm, got %v", newPerms["vm"])
		}
	})

	t.Run("vm_different_rejected", func(t *testing.T) {
		caller := &SSHKeyPerms{VM: "my-vm"}
		newPerms := map[string]any{"vm": "other-vm"}
		err := inheritCallerRestrictions(caller, newPerms)
		if err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("exp_inherited", func(t *testing.T) {
		exp := int64(9999999999)
		caller := &SSHKeyPerms{Exp: &exp}
		newPerms := map[string]any{}
		if err := inheritCallerRestrictions(caller, newPerms); err != nil {
			t.Fatal(err)
		}
		if newPerms["exp"] != exp {
			t.Errorf("expected exp=%d, got %v", exp, newPerms["exp"])
		}
	})

	t.Run("exp_clamped", func(t *testing.T) {
		callerExp := int64(1000)
		caller := &SSHKeyPerms{Exp: &callerExp}
		newPerms := map[string]any{"exp": int64(2000)}
		if err := inheritCallerRestrictions(caller, newPerms); err != nil {
			t.Fatal(err)
		}
		if newPerms["exp"] != callerExp {
			t.Errorf("expected exp clamped to %d, got %v", callerExp, newPerms["exp"])
		}
	})

	t.Run("exp_shorter_ok", func(t *testing.T) {
		callerExp := int64(2000)
		caller := &SSHKeyPerms{Exp: &callerExp}
		newPerms := map[string]any{"exp": int64(1000)}
		if err := inheritCallerRestrictions(caller, newPerms); err != nil {
			t.Fatal(err)
		}
		// New key's shorter expiry should be preserved.
		if newPerms["exp"] != int64(1000) {
			t.Errorf("expected exp=1000 (shorter), got %v", newPerms["exp"])
		}
	})

	t.Run("cmds_inherited", func(t *testing.T) {
		caller := &SSHKeyPerms{Cmds: []string{"ls", "whoami"}}
		newPerms := map[string]any{}
		if err := inheritCallerRestrictions(caller, newPerms); err != nil {
			t.Fatal(err)
		}
		cmds, ok := newPerms["cmds"].([]string)
		if !ok || len(cmds) != 2 {
			t.Errorf("expected cmds=[ls,whoami], got %v", newPerms["cmds"])
		}
	})

	t.Run("cmds_subset_ok", func(t *testing.T) {
		caller := &SSHKeyPerms{Cmds: []string{"ls", "whoami", "rm"}}
		newPerms := map[string]any{"cmds": []string{"ls", "whoami"}}
		if err := inheritCallerRestrictions(caller, newPerms); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("cmds_superset_rejected", func(t *testing.T) {
		caller := &SSHKeyPerms{Cmds: []string{"ls"}}
		newPerms := map[string]any{"cmds": []string{"ls", "rm"}}
		err := inheritCallerRestrictions(caller, newPerms)
		if err == nil {
			t.Fatal("expected error for superset cmds")
		}
		if !strings.Contains(err.Error(), "does not allow command") {
			t.Errorf("expected 'does not allow command' error, got: %v", err)
		}
	})

	t.Run("all_inherited", func(t *testing.T) {
		exp := int64(5000)
		caller := &SSHKeyPerms{
			Tag:  "ci",
			Cmds: []string{"ls", "whoami"},
			Exp:  &exp,
		}
		newPerms := map[string]any{}
		if err := inheritCallerRestrictions(caller, newPerms); err != nil {
			t.Fatal(err)
		}
		if newPerms["tag"] != "ci" {
			t.Error("tag not inherited")
		}
		if newPerms["exp"] != exp {
			t.Error("exp not inherited")
		}
		cmds, _ := newPerms["cmds"].([]string)
		if len(cmds) != 2 {
			t.Error("cmds not inherited")
		}
	})
}

// TestSSHKeyPermsTagScopeEnforcement tests that tag-scoped SSH key permissions
// are enforced in the executeCommandWithLogging path for ls.
func TestSSHKeyPermsTagScopeEnforcement(t *testing.T) {
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
			userID, pubStr, fp, `{"tag":"prod"}`); err != nil {
			return err
		}
		// Create two boxes: one tagged "prod", one untagged.
		if _, err := tx.Exec(`INSERT INTO boxes (ctrhost, name, status, image, created_by_user_id, tags, region) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			"ctr1", "prod-vm", "running", "exeuntu", userID, `["prod"]`, "pdx"); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO boxes (ctrhost, name, status, image, created_by_user_id, tags, region) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			"ctr1", "other-vm", "running", "exeuntu", userID, `[]`, "pdx"); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	ss := NewSSHServer(s)

	t.Run("ls_tag_scoped_filters", func(t *testing.T) {
		var buf strings.Builder
		cc := &exemenu.CommandContext{
			User:      &exemenu.UserInfo{ID: userID, Email: email},
			PublicKey: pubStr,
			Output:    &buf,
			Logger:    s.slog(),
		}
		rc := ss.executeCommandWithLogging(ctx, cc, []string{"ls", "--json"})
		if rc != 0 {
			t.Fatalf("expected rc=0, got %d: %s", rc, buf.String())
		}
		var result struct {
			VMs []struct {
				VMName string `json:"vm_name"`
			} `json:"vms"`
		}
		if err := json.Unmarshal([]byte(buf.String()), &result); err != nil {
			t.Fatalf("Decode: %v\n%s", err, buf.String())
		}
		if len(result.VMs) != 1 {
			t.Fatalf("expected 1 VM, got %d: %v", len(result.VMs), result.VMs)
		}
		if result.VMs[0].VMName != "prod-vm" {
			t.Errorf("expected prod-vm, got %s", result.VMs[0].VMName)
		}
	})

	t.Run("rm_tag_scoped_denied", func(t *testing.T) {
		var buf strings.Builder
		cc := &exemenu.CommandContext{
			User:      &exemenu.UserInfo{ID: userID, Email: email},
			PublicKey: pubStr,
			Output:    &buf,
			Logger:    s.slog(),
		}
		// Try to delete the untagged VM — rm writes per-VM errors but
		// still returns rc=0 when no VMs were successfully deleted.
		_ = ss.executeCommandWithLogging(ctx, cc, []string{"rm", "other-vm"})
		if !strings.Contains(buf.String(), "restricted to VMs with tag") {
			t.Errorf("expected tag restriction error, got: %s", buf.String())
		}
		// Verify the VM was NOT deleted by checking ls with an unrestricted key.
		pub2, _, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		sshPub2, _ := ssh.NewPublicKey(pub2)
		pubStr2 := string(ssh.MarshalAuthorizedKey(sshPub2))
		fp2 := strings.TrimPrefix(ssh.FingerprintSHA256(sshPub2), "SHA256:")
		err = s.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
			_, err := tx.Exec(`INSERT INTO ssh_keys (user_id, public_key, fingerprint) VALUES (?, ?, ?)`, userID, pubStr2, fp2)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		var buf2 strings.Builder
		cc2 := &exemenu.CommandContext{
			User:      &exemenu.UserInfo{ID: userID, Email: email},
			PublicKey: pubStr2,
			Output:    &buf2,
			Logger:    s.slog(),
		}
		rc := ss.executeCommandWithLogging(ctx, cc2, []string{"ls", "--json"})
		if rc != 0 {
			t.Fatalf("unrestricted ls failed: %s", buf2.String())
		}
		if !strings.Contains(buf2.String(), "other-vm") {
			t.Error("other-vm should still exist")
		}
	})

	t.Run("tag_command_blocked", func(t *testing.T) {
		var buf strings.Builder
		cc := &exemenu.CommandContext{
			User:      &exemenu.UserInfo{ID: userID, Email: email},
			PublicKey: pubStr,
			Output:    &buf,
			Logger:    s.slog(),
		}
		rc := ss.executeCommandWithLogging(ctx, cc, []string{"tag", "prod-vm", "extra"})
		if rc == 0 {
			t.Fatalf("expected failure for tag command with tag-scoped key: %s", buf.String())
		}
		if !strings.Contains(buf.String(), "cannot modify tags") {
			t.Errorf("expected tag modification error, got: %s", buf.String())
		}
	})
}

// TestSSHKeyTagScopedKeyVisibility verifies that tag-scoped keys can only
// see and remove other keys with the same tag, and cannot modify integrations.
func TestSSHKeyTagScopedKeyVisibility(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	ctx := t.Context()

	userID := "usr" + generateRegistrationToken()
	email := userID + "@test.example.com"

	// Create the tag-scoped key (ci).
	pub1, _, _ := ed25519.GenerateKey(rand.Reader)
	sshPub1, _ := ssh.NewPublicKey(pub1)
	pubStr1 := string(ssh.MarshalAuthorizedKey(sshPub1))
	fp1 := strings.TrimPrefix(ssh.FingerprintSHA256(sshPub1), "SHA256:")

	// Create a second tag-scoped key with the same tag.
	pub2, _, _ := ed25519.GenerateKey(rand.Reader)
	sshPub2, _ := ssh.NewPublicKey(pub2)
	pubStr2 := string(ssh.MarshalAuthorizedKey(sshPub2))
	fp2 := strings.TrimPrefix(ssh.FingerprintSHA256(sshPub2), "SHA256:")

	// Create an unrestricted key.
	pub3, _, _ := ed25519.GenerateKey(rand.Reader)
	sshPub3, _ := ssh.NewPublicKey(pub3)
	pubStr3 := string(ssh.MarshalAuthorizedKey(sshPub3))
	fp3 := strings.TrimPrefix(ssh.FingerprintSHA256(sshPub3), "SHA256:")

	err := s.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		if _, err := tx.Exec(`INSERT INTO users (user_id, email) VALUES (?, ?)`, userID, email); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO ssh_keys (user_id, public_key, fingerprint, comment, permissions) VALUES (?, ?, ?, ?, ?)`,
			userID, pubStr1, fp1, "ci-key-1", `{"tag":"ci"}`); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO ssh_keys (user_id, public_key, fingerprint, comment, permissions) VALUES (?, ?, ?, ?, ?)`,
			userID, pubStr2, fp2, "ci-key-2", `{"tag":"ci"}`); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO ssh_keys (user_id, public_key, fingerprint, comment) VALUES (?, ?, ?, ?)`,
			userID, pubStr3, fp3, "my-laptop"); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	ss := NewSSHServer(s)

	t.Run("list_only_shows_same_tag_keys", func(t *testing.T) {
		var buf strings.Builder
		cc := &exemenu.CommandContext{
			User:      &exemenu.UserInfo{ID: userID, Email: email},
			PublicKey: pubStr1,
			Output:    &buf,
			Logger:    s.slog(),
		}
		rc := ss.executeCommandWithLogging(ctx, cc, []string{"ssh-key", "list", "--json"})
		if rc != 0 {
			t.Fatalf("ssh-key list failed: %s", buf.String())
		}
		output := buf.String()
		// Should see both ci-key-1 and ci-key-2.
		if !strings.Contains(output, "ci-key-1") {
			t.Error("expected to see ci-key-1")
		}
		if !strings.Contains(output, "ci-key-2") {
			t.Error("expected to see ci-key-2")
		}
		// Should NOT see my-laptop.
		if strings.Contains(output, "my-laptop") {
			t.Error("tag-scoped key should NOT see unrestricted key 'my-laptop'")
		}
	})

	t.Run("remove_peer_tag_key_allowed", func(t *testing.T) {
		var buf strings.Builder
		cc := &exemenu.CommandContext{
			User:      &exemenu.UserInfo{ID: userID, Email: email},
			PublicKey: pubStr1,
			Output:    &buf,
			Logger:    s.slog(),
		}
		rc := ss.executeCommandWithLogging(ctx, cc, []string{"ssh-key", "remove", "ci-key-2"})
		if rc != 0 {
			t.Fatalf("expected success removing peer tag key, got rc=%d: %s", rc, buf.String())
		}
	})

	t.Run("remove_unrestricted_key_denied", func(t *testing.T) {
		var buf strings.Builder
		cc := &exemenu.CommandContext{
			User:      &exemenu.UserInfo{ID: userID, Email: email},
			PublicKey: pubStr1,
			Output:    &buf,
			Logger:    s.slog(),
		}
		rc := ss.executeCommandWithLogging(ctx, cc, []string{"ssh-key", "remove", "my-laptop"})
		if rc == 0 {
			t.Fatalf("expected failure removing unrestricted key, got success: %s", buf.String())
		}
		if !strings.Contains(buf.String(), "no matching SSH key found") {
			t.Errorf("expected 'no matching' error, got: %s", buf.String())
		}
	})

	t.Run("integrations_attach_denied", func(t *testing.T) {
		var buf strings.Builder
		cc := &exemenu.CommandContext{
			User:      &exemenu.UserInfo{ID: userID, Email: email},
			PublicKey: pubStr1,
			Output:    &buf,
			Logger:    s.slog(),
		}
		rc := ss.executeCommandWithLogging(ctx, cc, []string{"integrations", "add", "http-proxy", "--name=x"})
		if rc == 0 {
			t.Fatalf("expected integrations add to be denied for tag-scoped key: %s", buf.String())
		}
		if !strings.Contains(buf.String(), "tag-scoped SSH keys cannot modify integrations") {
			t.Errorf("expected integrations denial, got: %s", buf.String())
		}
	})
}

// TestSSHKeyDeleteSelfEscalation verifies that a restricted (tag-scoped) key
// cannot escalate privileges by deleting itself from the database.
// Before the fix, after deletion the per-command permission lookup returned
// (nil, nil) — treating the deleted key as unrestricted.
func TestSSHKeyDeleteSelfEscalation(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	ctx := t.Context()

	userID := "usr" + generateRegistrationToken()
	email := userID + "@test.example.com"

	// Generate the restricted key.
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
		if _, err := tx.Exec(`INSERT INTO ssh_keys (user_id, public_key, fingerprint, comment, permissions) VALUES (?, ?, ?, ?, ?)`,
			userID, pubStr, fp, "tag-key", `{"tag":"ci"}`); err != nil {
			return err
		}
		// Create two boxes: one tagged "ci", one untagged.
		if _, err := tx.Exec(`INSERT INTO boxes (ctrhost, name, status, image, created_by_user_id, tags, region) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			"ctr1", "ci-vm", "running", "exeuntu", userID, `["ci"]`, "pdx"); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO boxes (ctrhost, name, status, image, created_by_user_id, tags, region) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			"ctr1", "secret-vm", "running", "exeuntu", userID, `[]`, "pdx"); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	ss := NewSSHServer(s)

	// Simulate a REPL session: cache perms at session start.
	sessionPerms, err := s.getSSHKeyPermsByPublicKey(ctx, pubStr)
	if err != nil {
		t.Fatal(err)
	}
	if sessionPerms == nil || sessionPerms.Tag != "ci" {
		t.Fatal("expected tag-scoped perms at session start")
	}
	ctx = withSessionSSHKeyPerms(ctx, sessionPerms)

	// Verify tag scoping works: ls should only show ci-vm.
	t.Run("before_delete_scoped", func(t *testing.T) {
		var buf strings.Builder
		cc := &exemenu.CommandContext{
			User:      &exemenu.UserInfo{ID: userID, Email: email},
			PublicKey: pubStr,
			Output:    &buf,
			Logger:    s.slog(),
		}
		rc := ss.executeCommandWithLogging(ctx, cc, []string{"ls", "--json"})
		if rc != 0 {
			t.Fatalf("ls failed: %s", buf.String())
		}
		if strings.Contains(buf.String(), "secret-vm") {
			t.Error("tag-scoped key should NOT see secret-vm")
		}
		if !strings.Contains(buf.String(), "ci-vm") {
			t.Error("tag-scoped key should see ci-vm")
		}
	})

	// Now the attack: delete the restricted key from the DB (simulating
	// what `ssh-key remove tag-key` would do).
	err = s.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := tx.Exec(`DELETE FROM ssh_keys WHERE user_id = ? AND fingerprint = ?`, userID, fp)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	// After deletion, the key should be DENIED, not unrestricted.
	t.Run("after_delete_denied", func(t *testing.T) {
		var buf strings.Builder
		cc := &exemenu.CommandContext{
			User:      &exemenu.UserInfo{ID: userID, Email: email},
			PublicKey: pubStr,
			Output:    &buf,
			Logger:    s.slog(),
		}
		rc := ss.executeCommandWithLogging(ctx, cc, []string{"ls", "--json"})
		if rc == 0 {
			t.Fatalf("expected denial after key deletion, but command succeeded: %s", buf.String())
		}
		if !strings.Contains(buf.String(), "revoked") {
			t.Errorf("expected 'revoked' error, got: %s", buf.String())
		}
		// Specifically: secret-vm must NOT appear.
		if strings.Contains(buf.String(), "secret-vm") {
			t.Error("PRIVILEGE ESCALATION: deleted restricted key can see secret-vm")
		}
	})

	// Also verify that an unrestricted key being deleted does NOT cause denial
	// (for backward compatibility with proxy keys and normal key removal).
	t.Run("unrestricted_key_deleted_still_works", func(t *testing.T) {
		pub2, _, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		sshPub2, _ := ssh.NewPublicKey(pub2)
		pubStr2 := string(ssh.MarshalAuthorizedKey(sshPub2))
		fp2 := strings.TrimPrefix(ssh.FingerprintSHA256(sshPub2), "SHA256:")

		err = s.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
			_, err := tx.Exec(`INSERT INTO ssh_keys (user_id, public_key, fingerprint) VALUES (?, ?, ?)`, userID, pubStr2, fp2)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}

		// Start with no session perms (unrestricted key).
		ctx2 := t.Context()

		// Delete the key.
		err = s.db.Tx(ctx2, func(ctx context.Context, tx *sqlite.Tx) error {
			_, err := tx.Exec(`DELETE FROM ssh_keys WHERE user_id = ? AND fingerprint = ?`, userID, fp2)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}

		// Should still work (no session perms → no restriction to enforce).
		var buf strings.Builder
		cc := &exemenu.CommandContext{
			User:      &exemenu.UserInfo{ID: userID, Email: email},
			PublicKey: pubStr2,
			Output:    &buf,
			Logger:    s.slog(),
		}
		rc := ss.executeCommandWithLogging(ctx2, cc, []string{"ls", "--json"})
		if rc != 0 {
			t.Fatalf("unrestricted key deletion should not cause denial: %s", buf.String())
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
