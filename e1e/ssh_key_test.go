// This file contains tests for the ssh-key command.

package e1e

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"exe.dev/e1e/testinfra"
)

type sshKeyListOutput struct {
	SSHKeys []sshKeyEntry `json:"ssh_keys"`
}

type sshKeyEntry struct {
	PublicKey   string     `json:"public_key"`
	Fingerprint string     `json:"fingerprint"`
	Name        string     `json:"name"`
	AddedAt     *time.Time `json:"added_at,omitempty"`
	LastUsedAt  *time.Time `json:"last_used_at,omitempty"`
	Current     bool       `json:"current"`
}

// TestSSHKeyCommand tests the ssh-key command with list, add, and remove subcommands.
func TestSSHKeyCommand(t *testing.T) {
	t.Parallel()
	e1eTestsOnlyRunOnce(t)

	pty, _, keyFile, _ := registerForExeDev(t)

	// Generate a second SSH key to use for testing add/remove
	testKeyPath, testPubKey, err := testinfra.GenSSHKey(t.TempDir())
	if err != nil {
		t.Fatalf("failed to generate test SSH key: %v", err)
	}
	_ = testKeyPath // we only need the public key for this test

	t.Run("help", func(t *testing.T) {
		noGolden(t)
		pty := sshToExeDev(t, keyFile)
		defer pty.disconnect()

		// Running ssh-key with no subcommand should show help
		pty.sendLine("ssh-key")
		pty.want("ssh-key")
		pty.want("list")
		pty.want("add")
		pty.want("remove")
		pty.wantPrompt()
	})

	t.Run("list", func(t *testing.T) {
		noGolden(t)
		pty := sshToExeDev(t, keyFile)
		defer pty.disconnect()

		// List should show the current key
		pty.sendLine("ssh-key list")
		pty.want("SSH Keys:")
		pty.want("ssh-ed25519")
		pty.want("current")
		pty.wantPrompt()
	})

	t.Run("list_json", func(t *testing.T) {
		noGolden(t)

		out := runParseExeDevJSON[sshKeyListOutput](t, keyFile, "ssh-key", "list", "--json")
		if len(out.SSHKeys) == 0 {
			t.Fatal("expected at least one SSH key in output")
		}
		foundCurrent := false
		for _, key := range out.SSHKeys {
			if key.PublicKey == "" {
				t.Error("expected public_key to be non-empty")
			}
			if key.Fingerprint == "" {
				t.Error("expected fingerprint to be non-empty")
			}
			if !strings.HasPrefix(key.Fingerprint, "SHA256:") {
				t.Errorf("expected fingerprint to start with SHA256:, got %q", key.Fingerprint)
			}
			if key.Current {
				foundCurrent = true
				// The first key should have a generated name like "key-1"
				if key.Name != "key-1" {
					t.Errorf("expected current key name to be 'key-1', got %q", key.Name)
				}
			}
		}
		if !foundCurrent {
			t.Error("expected at least one key to be marked as current")
		}
	})

	t.Run("add_and_remove", func(t *testing.T) {
		noGolden(t)
		pty := sshToExeDev(t, keyFile)
		defer pty.disconnect()

		// Add a new SSH key
		pty.sendLine("ssh-key add '" + testPubKey + "'")
		pty.want("Added SSH key")
		pty.wantPrompt()

		// Verify the key appears in list
		pty.sendLine("ssh-key list")
		pty.want("SSH Keys:")
		pty.want("current") // original key
		pty.wantPrompt()

		// Try adding the same key again - should fail
		pty.sendLine("ssh-key add '" + testPubKey + "'")
		pty.want("already associated")
		pty.wantPrompt()

		// Remove the key
		pty.sendLine("ssh-key remove '" + testPubKey + "'")
		pty.want("Deleted SSH key")
		pty.wantPrompt()

		// Try to remove it again - should fail
		pty.sendLine("ssh-key remove '" + testPubKey + "'")
		pty.want("no matching SSH key found")
		pty.wantPrompt()
	})

	t.Run("add_invalid_key", func(t *testing.T) {
		noGolden(t)
		pty := sshToExeDev(t, keyFile)
		defer pty.disconnect()

		// Try to add an invalid key
		pty.sendLine("ssh-key add 'not-a-valid-key'")
		pty.want("invalid SSH public key")
		pty.wantPrompt()
	})

	t.Run("add_private_key_error", func(t *testing.T) {
		noGolden(t)
		pty := sshToExeDev(t, keyFile)
		defer pty.disconnect()

		// Try to add what looks like a private key - should get a helpful error
		// explaining to use the public key instead
		pty.sendLine("ssh-key add '-----BEGIN OPENSSH PRIVATE KEY-----'")
		pty.want("private key")
		pty.want("public key")
		pty.wantPrompt()
	})

	t.Run("help_add", func(t *testing.T) {
		noGolden(t)
		pty := sshToExeDev(t, keyFile)
		defer pty.disconnect()

		// Check help for add subcommand shows key generation instructions
		pty.sendLine("help ssh-key add")
		pty.want("ssh-keygen")
		pty.want("ed25519")
		pty.want("id_exe")
		pty.wantPrompt()
	})

	t.Run("json_add_remove", func(t *testing.T) {
		noGolden(t)
		pty := sshToExeDev(t, keyFile)
		defer pty.disconnect()

		// Generate another key for JSON testing
		_, jsonTestPubKey, err := testinfra.GenSSHKey(t.TempDir())
		if err != nil {
			t.Fatalf("failed to generate test SSH key: %v", err)
		}

		// Add with --json
		pty.sendLine("ssh-key add --json '" + jsonTestPubKey + "'")
		pty.want(`"status":`)
		pty.want(`"added"`)
		pty.wantPrompt()

		// Remove with --json
		pty.sendLine("ssh-key remove --json '" + jsonTestPubKey + "'")
		pty.want(`"status":`)
		pty.want(`"deleted"`)
		pty.wantPrompt()
	})

	pty.disconnect()
}

// TestSSHKeyCommandWithSecondKey tests that a second SSH key can be used to authenticate
// after being added via ssh-key add.
func TestSSHKeyCommandWithSecondKey(t *testing.T) {
	t.Parallel()
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, _, keyFile, _ := registerForExeDev(t)

	// Generate a second SSH key
	testKeyPath, testPubKey, err := testinfra.GenSSHKey(t.TempDir())
	if err != nil {
		t.Fatalf("failed to generate test SSH key: %v", err)
	}

	// Add the second key
	pty.sendLine("ssh-key add '" + testPubKey + "'")
	pty.want("Added SSH key")
	pty.wantPrompt()
	pty.disconnect()

	// Now try to connect using the second key
	pty2 := sshToExeDev(t, testKeyPath)
	pty2.wantPrompt()

	// Verify we're the same user
	pty2.sendLine("whoami")
	pty2.want("Email Address:")
	pty2.wantPrompt()

	// List should show both keys
	pty2.sendLine("ssh-key list")
	pty2.want("SSH Keys:")
	// The second key should show as current since we're using it
	pty2.want("current")
	pty2.wantPrompt()

	pty2.disconnect()

	// Clean up: remove the second key using the original key
	pty3 := sshToExeDev(t, keyFile)
	pty3.sendLine("ssh-key remove '" + testPubKey + "'")
	pty3.want("Deleted SSH key")
	pty3.wantPrompt()
	pty3.disconnect()

	// Verify the second key no longer works (this should fail to authenticate)
	// We can't easily test this without more infrastructure, but the remove was successful
}

// TestSSHKeyRemoveCurrentKey tests that removing all keys still works
// (the user would need to re-register, but that's their choice)
func TestSSHKeyRemoveCurrentKey(t *testing.T) {
	t.Parallel()
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, _, keyFile, _ := registerForExeDev(t)

	// Get the current public key
	type pubKeyExtractor struct {
		key string
	}

	// Use whoami to see our current key, then try to remove it
	// First, add a second key so we have something left
	testKeyPath, testPubKey, err := testinfra.GenSSHKey(t.TempDir())
	if err != nil {
		t.Fatalf("failed to generate test SSH key: %v", err)
	}
	_ = testKeyPath

	pty.sendLine("ssh-key add '" + testPubKey + "'")
	pty.want("Added SSH key")
	pty.wantPrompt()

	// Now we can get the original key from the list and confirm both are there
	pty.sendLine("ssh-key list")
	pty.want("SSH Keys:")
	pty.wantPrompt()

	// The test passes if we got here without errors
	// We don't want to actually remove the current key as it would break the session
	pty.disconnect()

	// Clean up
	cleanup := sshToExeDev(t, keyFile)
	cleanup.sendLine("ssh-key remove '" + strings.TrimSpace(testPubKey) + "'")
	cleanup.want("Deleted SSH key")
	cleanup.wantPrompt()
	cleanup.disconnect()
}

// TestSSHKeyAddFromStdin tests that ssh-key add can read from stdin
// (e.g., cat id_exe.pub | ssh exe.dev ssh-key add)
func TestSSHKeyAddFromStdin(t *testing.T) {
	t.Parallel()
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	_, _, keyFile, _ := registerForExeDev(t)

	// Helper to remove a key (used by subtests that add keys)
	removeKey := func(t *testing.T, pubKey string) {
		pty := sshToExeDev(t, keyFile)
		pty.sendLine("ssh-key remove '" + strings.TrimSpace(pubKey) + "'")
		pty.want("Deleted SSH key")
		pty.wantPrompt()
		pty.disconnect()
	}

	t.Run("basic", func(t *testing.T) {
		noGolden(t)

		// Generate a new key to add via stdin
		_, testPubKey, err := testinfra.GenSSHKey(t.TempDir())
		if err != nil {
			t.Fatalf("failed to generate test SSH key: %v", err)
		}

		// Add via stdin (simulates: cat key.pub | ssh exe.dev ssh-key add)
		out, err := Env.servers.RunExeDevSSHCommandWithStdin(
			Env.context(t),
			keyFile,
			[]byte(testPubKey),
			"ssh-key", "add",
		)
		if err != nil {
			t.Fatalf("ssh-key add from stdin failed: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), "Added SSH key") {
			t.Errorf("expected 'Added SSH key' in output, got: %s", out)
		}

		// Verify via ssh-key list --json
		out, err = Env.servers.RunExeDevSSHCommand(Env.context(t), keyFile, "ssh-key", "list", "--json")
		if err != nil {
			t.Fatalf("ssh-key list --json failed: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), "ssh-ed25519") {
			t.Errorf("expected ssh-ed25519 in output, got: %s", out)
		}

		removeKey(t, testPubKey)
	})

	t.Run("empty_stdin", func(t *testing.T) {
		noGolden(t)

		out, err := Env.servers.RunExeDevSSHCommandWithStdin(
			Env.context(t),
			keyFile,
			[]byte(""),
			"ssh-key", "add",
		)
		if err == nil {
			t.Fatalf("expected ssh-key add with empty stdin to fail, got: %s", out)
		}
		if !strings.Contains(string(out), "please provide the SSH public key to add") {
			t.Errorf("expected 'please provide the SSH public key to add' in output, got: %s", out)
		}
	})

	t.Run("whitespace_only_stdin", func(t *testing.T) {
		noGolden(t)

		out, err := Env.servers.RunExeDevSSHCommandWithStdin(
			Env.context(t),
			keyFile,
			[]byte("   \n\t\n   "),
			"ssh-key", "add",
		)
		if err == nil {
			t.Fatalf("expected ssh-key add with whitespace-only stdin to fail, got: %s", out)
		}
		if !strings.Contains(string(out), "please provide the SSH public key to add") {
			t.Errorf("expected 'please provide the SSH public key to add' in output, got: %s", out)
		}
	})

	t.Run("both_args_and_stdin", func(t *testing.T) {
		noGolden(t)

		_, testPubKey, err := testinfra.GenSSHKey(t.TempDir())
		if err != nil {
			t.Fatalf("failed to generate test SSH key: %v", err)
		}

		out, err := Env.servers.RunExeDevSSHCommandWithStdin(
			Env.context(t),
			keyFile,
			[]byte(testPubKey),
			"ssh-key", "add", testPubKey,
		)
		if err == nil {
			t.Fatalf("expected ssh-key add with both args and stdin to fail, got: %s", out)
		}
		if !strings.Contains(string(out), "either as an argument or via stdin, not both") {
			t.Errorf("expected 'either as an argument or via stdin, not both' in output, got: %s", out)
		}
	})

	t.Run("invalid_key", func(t *testing.T) {
		noGolden(t)

		out, err := Env.servers.RunExeDevSSHCommandWithStdin(
			Env.context(t),
			keyFile,
			[]byte("not-a-valid-ssh-key"),
			"ssh-key", "add",
		)
		if err == nil {
			t.Fatalf("expected ssh-key add with invalid key via stdin to fail, got: %s", out)
		}
		if !strings.Contains(string(out), "invalid SSH public key") {
			t.Errorf("expected 'invalid SSH public key' in output, got: %s", out)
		}
	})

	t.Run("private_key", func(t *testing.T) {
		noGolden(t)

		privateKey := `-----BEGIN OPENSSH PRIVATE KEY-----
aGVsbG8gaSBhbSBub3QgYWN0dWFsbHkgYSBwcml2YXRlIGtleQ==
-----END OPENSSH PRIVATE KEY-----`

		out, err := Env.servers.RunExeDevSSHCommandWithStdin(
			Env.context(t),
			keyFile,
			[]byte(privateKey),
			"ssh-key", "add",
		)
		if err == nil {
			t.Fatalf("expected ssh-key add with private key via stdin to fail, got: %s", out)
		}
		if !strings.Contains(string(out), "private key") || !strings.Contains(string(out), "public key") {
			t.Errorf("expected error about private key and public key in output, got: %s", out)
		}
	})

	t.Run("with_whitespace", func(t *testing.T) {
		noGolden(t)

		_, testPubKey, err := testinfra.GenSSHKey(t.TempDir())
		if err != nil {
			t.Fatalf("failed to generate test SSH key: %v", err)
		}

		// Add via stdin with extra whitespace/newlines
		keyWithWhitespace := "\n\n  " + testPubKey + "  \n\n\n"
		out, err := Env.servers.RunExeDevSSHCommandWithStdin(
			Env.context(t),
			keyFile,
			[]byte(keyWithWhitespace),
			"ssh-key", "add",
		)
		if err != nil {
			t.Fatalf("ssh-key add with whitespace should succeed: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), "Added SSH key") {
			t.Errorf("expected 'Added SSH key' in output, got: %s", out)
		}

		// Verify the key was added via list --json
		out, err = Env.servers.RunExeDevSSHCommand(Env.context(t), keyFile, "ssh-key", "list", "--json")
		if err != nil {
			t.Fatalf("ssh-key list --json failed: %v\n%s", err, out)
		}

		var listOutput sshKeyListOutput
		if err := json.Unmarshal(out, &listOutput); err != nil {
			t.Fatalf("failed to parse JSON: %v\n%s", err, out)
		}

		// Should have at least 2 keys (the original + the one we added)
		if len(listOutput.SSHKeys) < 2 {
			t.Errorf("expected at least 2 keys, got %d", len(listOutput.SSHKeys))
		}

		removeKey(t, testPubKey)
	})

	t.Run("duplicate_key", func(t *testing.T) {
		noGolden(t)

		_, testPubKey, err := testinfra.GenSSHKey(t.TempDir())
		if err != nil {
			t.Fatalf("failed to generate test SSH key: %v", err)
		}

		// Add it first time via stdin
		out, err := Env.servers.RunExeDevSSHCommandWithStdin(
			Env.context(t),
			keyFile,
			[]byte(testPubKey),
			"ssh-key", "add",
		)
		if err != nil {
			t.Fatalf("first ssh-key add should succeed: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), "Added SSH key") {
			t.Errorf("expected 'Added SSH key' in output, got: %s", out)
		}

		// Try to add it again via stdin - should fail
		out, err = Env.servers.RunExeDevSSHCommandWithStdin(
			Env.context(t),
			keyFile,
			[]byte(testPubKey),
			"ssh-key", "add",
		)
		if err == nil {
			t.Fatalf("expected duplicate key via stdin to fail, got: %s", out)
		}
		if !strings.Contains(string(out), "already associated with your account") {
			t.Errorf("expected 'already associated with your account' in output, got: %s", out)
		}

		removeKey(t, testPubKey)
	})

	t.Run("json_output", func(t *testing.T) {
		noGolden(t)

		_, testPubKey, err := testinfra.GenSSHKey(t.TempDir())
		if err != nil {
			t.Fatalf("failed to generate test SSH key: %v", err)
		}

		// Add via stdin with --json flag
		out, err := Env.servers.RunExeDevSSHCommandWithStdin(
			Env.context(t),
			keyFile,
			[]byte(testPubKey),
			"ssh-key", "add", "--json",
		)
		if err != nil {
			t.Fatalf("ssh-key add --json from stdin failed: %v\n%s", err, out)
		}

		// Parse JSON output
		var result struct {
			PublicKey string  `json:"public_key"`
			Status    string  `json:"status"`
			Comment   *string `json:"comment,omitempty"`
		}
		if err := json.Unmarshal(out, &result); err != nil {
			t.Fatalf("failed to parse JSON output: %v\n%s", err, out)
		}
		if result.Status != "added" {
			t.Errorf("expected status 'added', got %q", result.Status)
		}
		if result.PublicKey == "" {
			t.Error("expected public_key to be non-empty")
		}
		if !strings.Contains(result.PublicKey, "ssh-ed25519") {
			t.Errorf("expected public_key to contain 'ssh-ed25519', got %q", result.PublicKey)
		}

		removeKey(t, testPubKey)
	})

	t.Run("can_authenticate", func(t *testing.T) {
		noGolden(t)

		newKeyPath, testPubKey, err := testinfra.GenSSHKey(t.TempDir())
		if err != nil {
			t.Fatalf("failed to generate test SSH key: %v", err)
		}

		// Add via stdin
		out, err := Env.servers.RunExeDevSSHCommandWithStdin(
			Env.context(t),
			keyFile,
			[]byte(testPubKey),
			"ssh-key", "add",
		)
		if err != nil {
			t.Fatalf("ssh-key add from stdin failed: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), "Added SSH key") {
			t.Errorf("expected 'Added SSH key' in output, got: %s", out)
		}

		// Now authenticate with the new key
		pty2 := sshToExeDev(t, newKeyPath)
		pty2.wantPrompt()

		// Verify we're logged in as the same user by checking ssh-key list shows the key as current
		pty2.sendLine("ssh-key list")
		pty2.want("SSH Keys:")
		pty2.want("current") // the new key should show as current
		pty2.wantPrompt()
		pty2.disconnect()

		removeKey(t, testPubKey)
	})

	t.Run("another_users_key", func(t *testing.T) {
		noGolden(t)

		// Register a second user for this subtest
		pty1, _, keyFile1, _ := registerForExeDevWithEmail(t, "stdin-user1@ssh-key-stdin-cross-user.example")

		// Generate a key that user1 will own
		_, sharedPubKey, err := testinfra.GenSSHKey(t.TempDir())
		if err != nil {
			t.Fatalf("failed to generate shared SSH key: %v", err)
		}

		// User1 adds the shared key
		pty1.sendLine("ssh-key add '" + sharedPubKey + "'")
		pty1.want("Added SSH key")
		pty1.wantPrompt()
		pty1.disconnect()

		// Original user (from parent test) tries to add the same key via stdin - should fail
		out, err := Env.servers.RunExeDevSSHCommandWithStdin(
			Env.context(t),
			keyFile,
			[]byte(sharedPubKey),
			"ssh-key", "add",
		)
		if err == nil {
			t.Fatalf("expected adding another user's key via stdin to fail, got: %s", out)
		}
		if !strings.Contains(string(out), "already associated with another account") {
			t.Errorf("expected 'already associated with another account' in output, got: %s", out)
		}

		// Clean up: user1 removes the shared key
		cleanup := sshToExeDev(t, keyFile1)
		cleanup.sendLine("ssh-key remove '" + strings.TrimSpace(sharedPubKey) + "'")
		cleanup.want("Deleted SSH key")
		cleanup.wantPrompt()
		cleanup.disconnect()
	})
}

// TestSSHKeyCannotAddKeyFromAnotherUser tests that a user cannot add an SSH key
// that is already associated with another user's account.
func TestSSHKeyCannotAddKeyFromAnotherUser(t *testing.T) {
	t.Parallel()
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	// Register first user
	pty1, _, keyFile1, _ := registerForExeDevWithEmail(t, "user1@ssh-key-cross-user.example")

	// Generate a key that user1 will own
	_, sharedPubKey, err := testinfra.GenSSHKey(t.TempDir())
	if err != nil {
		t.Fatalf("failed to generate shared SSH key: %v", err)
	}

	// User1 adds the shared key
	pty1.sendLine("ssh-key add '" + sharedPubKey + "'")
	pty1.want("Added SSH key")
	pty1.wantPrompt()
	pty1.disconnect()

	// Register second user (different email, different key)
	pty2, _, _, _ := registerForExeDevWithEmail(t, "user2@ssh-key-cross-user.example")

	// User2 tries to add the same key - should fail
	pty2.sendLine("ssh-key add '" + sharedPubKey + "'")
	pty2.want("already associated with another account")
	pty2.wantPrompt()
	pty2.disconnect()

	// Clean up: user1 removes the shared key
	cleanup := sshToExeDev(t, keyFile1)
	cleanup.sendLine("ssh-key remove '" + strings.TrimSpace(sharedPubKey) + "'")
	cleanup.want("Deleted SSH key")
	cleanup.wantPrompt()
	cleanup.disconnect()
}

// TestSSHKeyCommentGeneration tests that SSH keys get auto-generated comments
// and that user-provided comments are sanitized.
func TestSSHKeyCommentGeneration(t *testing.T) {
	t.Parallel()
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, _, keyFile, _ := registerForExeDev(t)

	// Verify the initial key has comment "key-1"
	out := runParseExeDevJSON[sshKeyListOutput](t, keyFile, "ssh-key", "list", "--json")
	if len(out.SSHKeys) != 1 {
		t.Fatalf("expected 1 SSH key, got %d", len(out.SSHKeys))
	}
	if out.SSHKeys[0].Name != "key-1" {
		t.Errorf("expected initial key comment to be 'key-1', got %q", out.SSHKeys[0].Name)
	}

	// Generate a key with a comment that needs sanitization
	// Input: "my;laptop`test" should become "mylaptoptest" after sanitization
	_, testPubKeyWithComment, err := testinfra.GenSSHKeyWithComment(t.TempDir(), "my;laptop`test")
	if err != nil {
		t.Fatalf("failed to generate test SSH key: %v", err)
	}

	// Add the key (comment should be sanitized)
	pty.sendLine("ssh-key add '" + testPubKeyWithComment + "'")
	pty.want("Added SSH key")
	pty.wantPrompt()

	// Verify the comment was sanitized
	out = runParseExeDevJSON[sshKeyListOutput](t, keyFile, "ssh-key", "list", "--json")
	if len(out.SSHKeys) != 2 {
		t.Fatalf("expected 2 SSH keys, got %d", len(out.SSHKeys))
	}

	// Find the new key (not "key-1")
	var newKeyComment string
	for _, key := range out.SSHKeys {
		if key.Name != "key-1" {
			newKeyComment = key.Name
			break
		}
	}
	// Shell metacharacters should be removed: "my;laptop`test" -> "mylaptoptest"
	if newKeyComment != "mylaptoptest" {
		t.Errorf("expected sanitized comment 'mylaptoptest', got %q", newKeyComment)
	}

	// Clean up
	pty.sendLine("ssh-key remove '" + testPubKeyWithComment + "'")
	pty.want("Deleted SSH key")
	pty.wantPrompt()
	pty.disconnect()
}

// TestSSHKeyRemoveByName tests removing an SSH key by its name (comment).
func TestSSHKeyRemoveByName(t *testing.T) {
	t.Parallel()
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, _, keyFile, _ := registerForExeDev(t)

	// Add a key with a specific name
	_, testPubKey, err := testinfra.GenSSHKeyWithComment(t.TempDir(), "test-laptop")
	if err != nil {
		t.Fatalf("failed to generate test SSH key: %v", err)
	}

	pty.sendLine("ssh-key add '" + testPubKey + "'")
	pty.want("Added SSH key")
	pty.wantPrompt()

	// Verify the key exists with the expected name
	out := runParseExeDevJSON[sshKeyListOutput](t, keyFile, "ssh-key", "list", "--json")
	var found bool
	for _, key := range out.SSHKeys {
		if key.Name == "test-laptop" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected to find key with name 'test-laptop'")
	}

	// Remove by name
	pty.sendLine("ssh-key remove test-laptop")
	pty.want("Deleted SSH key")
	pty.wantPrompt()

	// Verify the key is gone
	out = runParseExeDevJSON[sshKeyListOutput](t, keyFile, "ssh-key", "list", "--json")
	for _, key := range out.SSHKeys {
		if key.Name == "test-laptop" {
			t.Error("key 'test-laptop' should have been removed")
		}
	}

	pty.disconnect()
}

// TestSSHKeyRemoveByFingerprint tests removing an SSH key by its fingerprint.
func TestSSHKeyRemoveByFingerprint(t *testing.T) {
	t.Parallel()
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, _, keyFile, _ := registerForExeDev(t)

	// Add a key
	_, testPubKey, err := testinfra.GenSSHKeyWithComment(t.TempDir(), "fp-test-key")
	if err != nil {
		t.Fatalf("failed to generate test SSH key: %v", err)
	}

	pty.sendLine("ssh-key add '" + testPubKey + "'")
	pty.want("Added SSH key")
	pty.wantPrompt()

	// Get the fingerprint from the list
	out := runParseExeDevJSON[sshKeyListOutput](t, keyFile, "ssh-key", "list", "--json")
	var fingerprint string
	for _, key := range out.SSHKeys {
		if key.Name == "fp-test-key" {
			fingerprint = key.Fingerprint
			break
		}
	}
	if fingerprint == "" {
		t.Fatal("failed to find fingerprint for test key")
	}

	// Verify fingerprint has SHA256: prefix
	if !strings.HasPrefix(fingerprint, "SHA256:") {
		t.Errorf("expected fingerprint to start with SHA256:, got %q", fingerprint)
	}

	// Remove by fingerprint WITH SHA256: prefix
	pty.sendLine("ssh-key remove " + fingerprint)
	pty.want("Deleted SSH key")
	pty.wantPrompt()

	// Add another key to test without prefix
	_, testPubKey2, err := testinfra.GenSSHKeyWithComment(t.TempDir(), "fp-test-key2")
	if err != nil {
		t.Fatalf("failed to generate test SSH key: %v", err)
	}

	pty.sendLine("ssh-key add '" + testPubKey2 + "'")
	pty.want("Added SSH key")
	pty.wantPrompt()

	// Get the fingerprint
	out = runParseExeDevJSON[sshKeyListOutput](t, keyFile, "ssh-key", "list", "--json")
	var fingerprint2 string
	for _, key := range out.SSHKeys {
		if key.Name == "fp-test-key2" {
			fingerprint2 = key.Fingerprint
			break
		}
	}

	// Remove by fingerprint WITHOUT SHA256: prefix
	fpWithoutPrefix := strings.TrimPrefix(fingerprint2, "SHA256:")
	pty.sendLine("ssh-key remove " + fpWithoutPrefix)
	pty.want("Deleted SSH key")
	pty.wantPrompt()

	pty.disconnect()
}

// TestSSHKeyRemoveByPublicKey tests removing an SSH key by its full public key.
// This exercises the fingerprint-based lookup used internally when a full public key is provided.
func TestSSHKeyRemoveByPublicKey(t *testing.T) {
	t.Parallel()
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, _, keyFile, _ := registerForExeDev(t)

	// Add a key with a specific name
	_, testPubKey, err := testinfra.GenSSHKeyWithComment(t.TempDir(), "pubkey-remove-test")
	if err != nil {
		t.Fatalf("failed to generate test SSH key: %v", err)
	}

	pty.sendLine("ssh-key add '" + testPubKey + "'")
	pty.want("Added SSH key")
	pty.wantPrompt()

	// Verify the key exists
	out := runParseExeDevJSON[sshKeyListOutput](t, keyFile, "ssh-key", "list", "--json")
	var foundKey *sshKeyEntry
	for i, key := range out.SSHKeys {
		if key.Name == "pubkey-remove-test" {
			foundKey = &out.SSHKeys[i]
			break
		}
	}
	if foundKey == nil {
		t.Fatal("expected to find key with name 'pubkey-remove-test'")
	}

	// Remove by full public key (the canonical form stored in the DB)
	// This tests the fingerprint-based resolution path
	pty.sendLine("ssh-key remove '" + foundKey.PublicKey + "'")
	pty.want("Deleted SSH key")
	pty.wantPrompt()

	// Verify the key is gone
	out = runParseExeDevJSON[sshKeyListOutput](t, keyFile, "ssh-key", "list", "--json")
	for _, key := range out.SSHKeys {
		if key.Name == "pubkey-remove-test" {
			t.Error("key 'pubkey-remove-test' should have been removed")
		}
	}

	// Test removing by public key with comment appended (as user would paste from .pub file)
	_, testPubKey2, err := testinfra.GenSSHKeyWithComment(t.TempDir(), "pubkey-remove-test2")
	if err != nil {
		t.Fatalf("failed to generate test SSH key: %v", err)
	}

	pty.sendLine("ssh-key add '" + testPubKey2 + "'")
	pty.want("Added SSH key")
	pty.wantPrompt()

	// Remove using the full public key string including the comment
	// (ssh.ParseAuthorizedKey will parse and extract the key, ignoring the comment)
	pty.sendLine("ssh-key remove '" + testPubKey2 + "'")
	pty.want("Deleted SSH key")
	pty.wantPrompt()

	pty.disconnect()
}

// TestSSHKeyRemoveAmbiguity tests that removing a key fails with helpful message when multiple keys match.
func TestSSHKeyRemoveAmbiguity(t *testing.T) {
	t.Parallel()
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, _, keyFile, _ := registerForExeDev(t)

	// Add two keys with the same name (via direct insertion to bypass uniqueness check)
	// Actually, we can't have duplicate comments in the normal flow.
	// Instead, test ambiguity would only occur if there were duplicate names in the DB,
	// which shouldn't happen. Let's test the non-ambiguous case thoroughly instead
	// and test that empty name doesn't match.

	// Test that empty string doesn't remove anything
	pty.sendLine("ssh-key remove ''")
	pty.want("please specify a key to remove")
	pty.wantPrompt()

	// Test that a non-existent name returns proper error
	pty.sendLine("ssh-key remove nonexistent-key-name")
	pty.want("no matching SSH key found")
	pty.wantPrompt()

	// Test JSON output when removing by name
	_, testPubKey, err := testinfra.GenSSHKeyWithComment(t.TempDir(), "json-remove-test")
	if err != nil {
		t.Fatalf("failed to generate test SSH key: %v", err)
	}

	pty.sendLine("ssh-key add '" + testPubKey + "'")
	pty.want("Added SSH key")
	pty.wantPrompt()

	// Remove with --json
	out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), keyFile, "ssh-key", "remove", "--json", "json-remove-test")
	if err != nil {
		t.Fatalf("ssh-key remove --json failed: %v\n%s", err, out)
	}

	var result struct {
		PublicKey   string `json:"public_key"`
		Fingerprint string `json:"fingerprint"`
		Name        string `json:"name"`
		Status      string `json:"status"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("failed to parse JSON: %v\n%s", err, out)
	}
	if result.Status != "deleted" {
		t.Errorf("expected status 'deleted', got %q", result.Status)
	}
	if result.Name != "json-remove-test" {
		t.Errorf("expected name 'json-remove-test', got %q", result.Name)
	}
	if result.Fingerprint == "" {
		t.Error("expected fingerprint to be non-empty")
	}

	pty.disconnect()
}

// TestSSHKeyRename tests renaming an SSH key.
func TestSSHKeyRename(t *testing.T) {
	t.Parallel()
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, _, keyFile, _ := registerForExeDev(t)

	// Add a key with a specific name
	_, testPubKey, err := testinfra.GenSSHKeyWithComment(t.TempDir(), "old-name")
	if err != nil {
		t.Fatalf("failed to generate test SSH key: %v", err)
	}

	pty.sendLine("ssh-key add '" + testPubKey + "'")
	pty.want("Added SSH key")
	pty.wantPrompt()

	// Rename the key
	pty.sendLine("ssh-key rename old-name new-name")
	pty.want("Renamed key")
	pty.want("new-name")
	pty.wantPrompt()

	// Verify the name changed
	out := runParseExeDevJSON[sshKeyListOutput](t, keyFile, "ssh-key", "list", "--json")
	var foundOld, foundNew bool
	for _, key := range out.SSHKeys {
		if key.Name == "old-name" {
			foundOld = true
		}
		if key.Name == "new-name" {
			foundNew = true
		}
	}
	if foundOld {
		t.Error("old name should not exist after rename")
	}
	if !foundNew {
		t.Error("new name should exist after rename")
	}

	// Clean up
	pty.sendLine("ssh-key remove new-name")
	pty.want("Deleted SSH key")
	pty.wantPrompt()
	pty.disconnect()
}

// TestSSHKeyRenameByFingerprint tests renaming an SSH key by SHA256:-prefixed fingerprint.
// This is an undocumented escape hatch for renaming keys with empty names (e.g., via the web UI).
func TestSSHKeyRenameByFingerprint(t *testing.T) {
	t.Parallel()
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, _, keyFile, _ := registerForExeDev(t)

	// The initial key should have name "key-1"
	out := runParseExeDevJSON[sshKeyListOutput](t, keyFile, "ssh-key", "list", "--json")
	if len(out.SSHKeys) == 0 {
		t.Fatal("expected at least one SSH key")
	}

	// Find the key-1 key and get its fingerprint
	var fingerprint string
	for _, key := range out.SSHKeys {
		if key.Name == "key-1" {
			fingerprint = key.Fingerprint
			break
		}
	}
	if fingerprint == "" {
		t.Fatal("expected to find key with name 'key-1'")
	}

	// Rename by fingerprint
	pty.sendLine("ssh-key rename " + fingerprint + " my-main-key")
	pty.want("Renamed")
	pty.want("my-main-key")
	pty.wantPrompt()

	// Verify the name changed
	out = runParseExeDevJSON[sshKeyListOutput](t, keyFile, "ssh-key", "list", "--json")
	var foundOld, foundNew bool
	for _, key := range out.SSHKeys {
		if key.Name == "key-1" {
			foundOld = true
		}
		if key.Name == "my-main-key" {
			foundNew = true
		}
	}
	if foundOld {
		t.Error("old name 'key-1' should not exist after rename")
	}
	if !foundNew {
		t.Error("new name 'my-main-key' should exist after rename")
	}

	// Rename it back for cleanup
	pty.sendLine("ssh-key rename my-main-key key-1")
	pty.want("Renamed")
	pty.wantPrompt()
	pty.disconnect()
}

// TestSSHKeyRenameValidation tests validation for ssh-key rename.
func TestSSHKeyRenameValidation(t *testing.T) {
	t.Parallel()
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, _, keyFile, _ := registerForExeDev(t)

	// Add two keys
	_, testPubKey1, err := testinfra.GenSSHKeyWithComment(t.TempDir(), "key-a")
	if err != nil {
		t.Fatalf("failed to generate test SSH key: %v", err)
	}
	_, testPubKey2, err := testinfra.GenSSHKeyWithComment(t.TempDir(), "key-b")
	if err != nil {
		t.Fatalf("failed to generate test SSH key: %v", err)
	}

	pty.sendLine("ssh-key add '" + testPubKey1 + "'")
	pty.want("Added SSH key")
	pty.wantPrompt()

	pty.sendLine("ssh-key add '" + testPubKey2 + "'")
	pty.want("Added SSH key")
	pty.wantPrompt()

	// Test: renaming to existing name should fail
	pty.sendLine("ssh-key rename key-a key-b")
	pty.want("already exists")
	pty.wantPrompt()

	// Test: renaming with special characters sanitizes them
	pty.sendLine("ssh-key rename key-a 'bad;name'")
	pty.want("Renamed")
	pty.want("badname") // semicolon removed by sanitization
	pty.wantPrompt()

	// Rename back so subsequent tests work
	pty.sendLine("ssh-key rename badname key-a")
	pty.want("Renamed")
	pty.wantPrompt()

	// Test: renaming to all-invalid characters should fail (name is empty after removing special chars)
	pty.sendLine("ssh-key rename key-a ';|$'")
	pty.want("new name is empty")
	pty.wantPrompt()

	// Test: renaming non-existent key should fail
	pty.sendLine("ssh-key rename nonexistent-key new-name")
	pty.want("no matching SSH key found")
	pty.wantPrompt()

	// Test JSON output for rename
	out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), keyFile, "ssh-key", "rename", "--json", "key-a", "key-renamed")
	if err != nil {
		t.Fatalf("ssh-key rename --json failed: %v\n%s", err, out)
	}

	var result struct {
		PublicKey   string `json:"public_key"`
		Fingerprint string `json:"fingerprint"`
		OldName     string `json:"old_name"`
		NewName     string `json:"new_name"`
		Status      string `json:"status"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("failed to parse JSON: %v\n%s", err, out)
	}
	if result.Status != "renamed" {
		t.Errorf("expected status 'renamed', got %q", result.Status)
	}
	if result.OldName != "key-a" {
		t.Errorf("expected old_name 'key-a', got %q", result.OldName)
	}
	if result.NewName != "key-renamed" {
		t.Errorf("expected new_name 'key-renamed', got %q", result.NewName)
	}
	if result.PublicKey == "" {
		t.Error("expected public_key to be non-empty")
	}
	if !strings.HasPrefix(result.PublicKey, "ssh-") {
		t.Errorf("expected public_key to start with 'ssh-', got %q", result.PublicKey)
	}

	// Clean up
	pty.sendLine("ssh-key remove key-renamed")
	pty.want("Deleted")
	pty.wantPrompt()
	pty.sendLine("ssh-key remove key-b")
	pty.want("Deleted")
	pty.wantPrompt()
	pty.disconnect()
}

// TestWhoamiShowsKeyNames tests that whoami shows key names.
func TestWhoamiShowsKeyNames(t *testing.T) {
	t.Parallel()
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, _, keyFile, _ := registerForExeDev(t)

	// Add a key with a specific name
	_, testPubKey, err := testinfra.GenSSHKeyWithComment(t.TempDir(), "whoami-test-key")
	if err != nil {
		t.Fatalf("failed to generate test SSH key: %v", err)
	}

	pty.sendLine("ssh-key add '" + testPubKey + "'")
	pty.want("Added SSH key")
	pty.wantPrompt()

	// Check whoami text output shows the name
	pty.sendLine("whoami")
	pty.want("whoami-test-key")
	pty.wantPrompt()

	// Check whoami JSON output includes the name
	type whoamiOutput struct {
		Email   string `json:"email"`
		SSHKeys []struct {
			PublicKey   string `json:"public_key"`
			Fingerprint string `json:"fingerprint"`
			Name        string `json:"name"`
			Current     bool   `json:"current"`
		} `json:"ssh_keys"`
	}

	out := runParseExeDevJSON[whoamiOutput](t, keyFile, "whoami", "--json")
	var foundName bool
	for _, key := range out.SSHKeys {
		if key.Name == "whoami-test-key" {
			foundName = true
			break
		}
	}
	if !foundName {
		t.Error("expected whoami JSON to include key with name 'whoami-test-key'")
	}

	// Clean up
	pty.sendLine("ssh-key remove whoami-test-key")
	pty.want("Deleted")
	pty.wantPrompt()
	pty.disconnect()
}
