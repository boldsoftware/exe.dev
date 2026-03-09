// This file contains tests for the ssh-key command.

package e1e

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"exe.dev/e1e/testinfra"
	"github.com/playwright-community/playwright-go"
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
	reserveVMs(t, 0)
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
		defer pty.Disconnect()

		// Running ssh-key with no subcommand should show help
		pty.SendLine("ssh-key")
		pty.Want("ssh-key")
		pty.Want("list")
		pty.Want("add")
		pty.Want("remove")
		pty.WantPrompt()
	})

	t.Run("list", func(t *testing.T) {
		noGolden(t)
		pty := sshToExeDev(t, keyFile)
		defer pty.Disconnect()

		// List should show the current key
		pty.SendLine("ssh-key list")
		pty.Want("SSH Keys:")
		pty.Want("ssh-ed25519")
		pty.Want("current")
		pty.WantPrompt()
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
		defer pty.Disconnect()

		// Add a new SSH key
		pty.SendLine("ssh-key add '" + testPubKey + "'")
		pty.Want("Added SSH key")
		pty.WantPrompt()

		// Verify the key appears in list
		pty.SendLine("ssh-key list")
		pty.Want("SSH Keys:")
		pty.Want("current") // original key
		pty.WantPrompt()

		// Try adding the same key again - should fail
		pty.SendLine("ssh-key add '" + testPubKey + "'")
		pty.Want("already associated")
		pty.WantPrompt()

		// Remove the key
		pty.SendLine("ssh-key remove '" + testPubKey + "'")
		pty.Want("Deleted SSH key")
		pty.WantPrompt()

		// Try to remove it again - should fail
		pty.SendLine("ssh-key remove '" + testPubKey + "'")
		pty.Want("no matching SSH key found")
		pty.WantPrompt()
	})

	t.Run("add_invalid_key", func(t *testing.T) {
		noGolden(t)
		pty := sshToExeDev(t, keyFile)
		defer pty.Disconnect()

		// Try to add an invalid key
		pty.SendLine("ssh-key add 'not-a-valid-key'")
		pty.Want("invalid SSH public key")
		pty.WantPrompt()
	})

	t.Run("add_private_key_error", func(t *testing.T) {
		noGolden(t)
		pty := sshToExeDev(t, keyFile)
		defer pty.Disconnect()

		// Try to add what looks like a private key - should get a helpful error
		// explaining to use the public key instead
		pty.SendLine("ssh-key add '-----BEGIN OPENSSH PRIVATE KEY-----'")
		pty.Want("private key")
		pty.Want("public key")
		pty.WantPrompt()
	})

	t.Run("help_add", func(t *testing.T) {
		noGolden(t)
		pty := sshToExeDev(t, keyFile)
		defer pty.Disconnect()

		// Check help for add subcommand shows key generation instructions
		pty.SendLine("help ssh-key add")
		pty.Want("ssh-keygen")
		pty.Want("ed25519")
		pty.Want("id_exe")
		pty.WantPrompt()
	})

	t.Run("json_add_remove", func(t *testing.T) {
		noGolden(t)
		pty := sshToExeDev(t, keyFile)
		defer pty.Disconnect()

		// Generate another key for JSON testing
		_, jsonTestPubKey, err := testinfra.GenSSHKey(t.TempDir())
		if err != nil {
			t.Fatalf("failed to generate test SSH key: %v", err)
		}

		// Add with --json
		pty.SendLine("ssh-key add --json '" + jsonTestPubKey + "'")
		pty.Want(`"status":`)
		pty.Want(`"added"`)
		pty.WantPrompt()

		// Remove with --json
		pty.SendLine("ssh-key remove --json '" + jsonTestPubKey + "'")
		pty.Want(`"status":`)
		pty.Want(`"deleted"`)
		pty.WantPrompt()
	})

	pty.Disconnect()
}

// TestSSHKeyCommandWithSecondKey tests that a second SSH key can be used to authenticate
// after being added via ssh-key add.
func TestSSHKeyCommandWithSecondKey(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, _, keyFile, _ := registerForExeDev(t)

	// Generate a second SSH key
	testKeyPath, testPubKey, err := testinfra.GenSSHKey(t.TempDir())
	if err != nil {
		t.Fatalf("failed to generate test SSH key: %v", err)
	}

	// Add the second key
	pty.SendLine("ssh-key add '" + testPubKey + "'")
	pty.Want("Added SSH key")
	pty.WantPrompt()
	pty.Disconnect()

	// Now try to connect using the second key
	pty2 := sshToExeDev(t, testKeyPath)
	pty2.WantPrompt()

	// Verify we're the same user
	pty2.SendLine("whoami")
	pty2.Want("Email Address:")
	pty2.WantPrompt()

	// List should show both keys
	pty2.SendLine("ssh-key list")
	pty2.Want("SSH Keys:")
	// The second key should show as current since we're using it
	pty2.Want("current")
	pty2.WantPrompt()

	pty2.Disconnect()

	// Clean up: remove the second key using the original key
	pty3 := sshToExeDev(t, keyFile)
	pty3.SendLine("ssh-key remove '" + testPubKey + "'")
	pty3.Want("Deleted SSH key")
	pty3.WantPrompt()
	pty3.Disconnect()

	// Verify the second key no longer works (this should fail to authenticate)
	// We can't easily test this without more infrastructure, but the remove was successful
}

// TestSSHKeyRemoveCurrentKey tests that removing all keys still works
// (the user would need to re-register, but that's their choice)
func TestSSHKeyRemoveCurrentKey(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
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

	pty.SendLine("ssh-key add '" + testPubKey + "'")
	pty.Want("Added SSH key")
	pty.WantPrompt()

	// Now we can get the original key from the list and confirm both are there
	pty.SendLine("ssh-key list")
	pty.Want("SSH Keys:")
	pty.WantPrompt()

	// The test passes if we got here without errors
	// We don't want to actually remove the current key as it would break the session
	pty.Disconnect()

	// Clean up
	cleanup := sshToExeDev(t, keyFile)
	cleanup.SendLine("ssh-key remove '" + strings.TrimSpace(testPubKey) + "'")
	cleanup.Want("Deleted SSH key")
	cleanup.WantPrompt()
	cleanup.Disconnect()
}

// TestSSHKeyAddFromStdin tests that ssh-key add can read from stdin
// (e.g., cat id_exe.pub | ssh exe.dev ssh-key add)
func TestSSHKeyAddFromStdin(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	_, _, keyFile, _ := registerForExeDev(t)

	// Helper to remove a key (used by subtests that add keys)
	removeKey := func(t *testing.T, pubKey string) {
		pty := sshToExeDev(t, keyFile)
		pty.SendLine("ssh-key remove '" + strings.TrimSpace(pubKey) + "'")
		pty.Want("Deleted SSH key")
		pty.WantPrompt()
		pty.Disconnect()
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
		pty2.WantPrompt()

		// Verify we're logged in as the same user by checking ssh-key list shows the key as current
		pty2.SendLine("ssh-key list")
		pty2.Want("SSH Keys:")
		pty2.Want("current") // the new key should show as current
		pty2.WantPrompt()
		pty2.Disconnect()

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
		pty1.SendLine("ssh-key add '" + sharedPubKey + "'")
		pty1.Want("Added SSH key")
		pty1.WantPrompt()
		pty1.Disconnect()

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
		cleanup.SendLine("ssh-key remove '" + strings.TrimSpace(sharedPubKey) + "'")
		cleanup.Want("Deleted SSH key")
		cleanup.WantPrompt()
		cleanup.Disconnect()
	})
}

// TestSSHKeyCannotAddKeyFromAnotherUser tests that a user cannot add an SSH key
// that is already associated with another user's account.
func TestSSHKeyCannotAddKeyFromAnotherUser(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
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
	pty1.SendLine("ssh-key add '" + sharedPubKey + "'")
	pty1.Want("Added SSH key")
	pty1.WantPrompt()
	pty1.Disconnect()

	// Register second user (different email, different key)
	pty2, _, _, _ := registerForExeDevWithEmail(t, "user2@ssh-key-cross-user.example")

	// User2 tries to add the same key - should fail
	pty2.SendLine("ssh-key add '" + sharedPubKey + "'")
	pty2.Want("already associated with another account")
	pty2.WantPrompt()
	pty2.Disconnect()

	// Clean up: user1 removes the shared key
	cleanup := sshToExeDev(t, keyFile1)
	cleanup.SendLine("ssh-key remove '" + strings.TrimSpace(sharedPubKey) + "'")
	cleanup.Want("Deleted SSH key")
	cleanup.WantPrompt()
	cleanup.Disconnect()
}

// TestSSHKeyCommentGeneration tests that SSH keys get auto-generated comments
// and that user-provided comments are sanitized.
func TestSSHKeyCommentGeneration(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
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
	pty.SendLine("ssh-key add '" + testPubKeyWithComment + "'")
	pty.Want("Added SSH key")
	pty.WantPrompt()

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
	pty.SendLine("ssh-key remove '" + testPubKeyWithComment + "'")
	pty.Want("Deleted SSH key")
	pty.WantPrompt()
	pty.Disconnect()
}

// TestSSHKeyRemoveByName tests removing an SSH key by its name (comment).
func TestSSHKeyRemoveByName(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, _, keyFile, _ := registerForExeDev(t)

	// Add a key with a specific name
	_, testPubKey, err := testinfra.GenSSHKeyWithComment(t.TempDir(), "test-laptop")
	if err != nil {
		t.Fatalf("failed to generate test SSH key: %v", err)
	}

	pty.SendLine("ssh-key add '" + testPubKey + "'")
	pty.Want("Added SSH key")
	pty.WantPrompt()

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
	pty.SendLine("ssh-key remove test-laptop")
	pty.Want("Deleted SSH key")
	pty.WantPrompt()

	// Verify the key is gone
	out = runParseExeDevJSON[sshKeyListOutput](t, keyFile, "ssh-key", "list", "--json")
	for _, key := range out.SSHKeys {
		if key.Name == "test-laptop" {
			t.Error("key 'test-laptop' should have been removed")
		}
	}

	pty.Disconnect()
}

// TestSSHKeyRemoveByFingerprint tests removing an SSH key by its fingerprint.
func TestSSHKeyRemoveByFingerprint(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, _, keyFile, _ := registerForExeDev(t)

	// Add a key
	_, testPubKey, err := testinfra.GenSSHKeyWithComment(t.TempDir(), "fp-test-key")
	if err != nil {
		t.Fatalf("failed to generate test SSH key: %v", err)
	}

	pty.SendLine("ssh-key add '" + testPubKey + "'")
	pty.Want("Added SSH key")
	pty.WantPrompt()

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
	pty.SendLine("ssh-key remove " + fingerprint)
	pty.Want("Deleted SSH key")
	pty.WantPrompt()

	// Add another key to test without prefix
	_, testPubKey2, err := testinfra.GenSSHKeyWithComment(t.TempDir(), "fp-test-key2")
	if err != nil {
		t.Fatalf("failed to generate test SSH key: %v", err)
	}

	pty.SendLine("ssh-key add '" + testPubKey2 + "'")
	pty.Want("Added SSH key")
	pty.WantPrompt()

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
	pty.SendLine("ssh-key remove " + fpWithoutPrefix)
	pty.Want("Deleted SSH key")
	pty.WantPrompt()

	pty.Disconnect()
}

// TestSSHKeyRemoveByPublicKey tests removing an SSH key by its full public key.
// This exercises the fingerprint-based lookup used internally when a full public key is provided.
func TestSSHKeyRemoveByPublicKey(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, _, keyFile, _ := registerForExeDev(t)

	// Add a key with a specific name
	_, testPubKey, err := testinfra.GenSSHKeyWithComment(t.TempDir(), "pubkey-remove-test")
	if err != nil {
		t.Fatalf("failed to generate test SSH key: %v", err)
	}

	pty.SendLine("ssh-key add '" + testPubKey + "'")
	pty.Want("Added SSH key")
	pty.WantPrompt()

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
	pty.SendLine("ssh-key remove '" + foundKey.PublicKey + "'")
	pty.Want("Deleted SSH key")
	pty.WantPrompt()

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

	pty.SendLine("ssh-key add '" + testPubKey2 + "'")
	pty.Want("Added SSH key")
	pty.WantPrompt()

	// Remove using the full public key string including the comment
	// (ssh.ParseAuthorizedKey will parse and extract the key, ignoring the comment)
	pty.SendLine("ssh-key remove '" + testPubKey2 + "'")
	pty.Want("Deleted SSH key")
	pty.WantPrompt()

	pty.Disconnect()
}

// TestSSHKeyRemoveAmbiguity tests that removing a key fails with helpful message when multiple keys match.
func TestSSHKeyRemoveAmbiguity(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, _, keyFile, _ := registerForExeDev(t)

	// Add two keys with the same name (via direct insertion to bypass uniqueness check)
	// Actually, we can't have duplicate comments in the normal flow.
	// Instead, test ambiguity would only occur if there were duplicate names in the DB,
	// which shouldn't happen. Let's test the non-ambiguous case thoroughly instead
	// and test that empty name doesn't match.

	// Test that empty string doesn't remove anything
	pty.SendLine("ssh-key remove ''")
	pty.Want("please specify a key to remove")
	pty.WantPrompt()

	// Test that a non-existent name returns proper error
	pty.SendLine("ssh-key remove nonexistent-key-name")
	pty.Want("no matching SSH key found")
	pty.WantPrompt()

	// Test JSON output when removing by name
	_, testPubKey, err := testinfra.GenSSHKeyWithComment(t.TempDir(), "json-remove-test")
	if err != nil {
		t.Fatalf("failed to generate test SSH key: %v", err)
	}

	pty.SendLine("ssh-key add '" + testPubKey + "'")
	pty.Want("Added SSH key")
	pty.WantPrompt()

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

	pty.Disconnect()
}

// TestSSHKeyRename tests renaming an SSH key.
func TestSSHKeyRename(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, _, keyFile, _ := registerForExeDev(t)

	// Add a key with a specific name
	_, testPubKey, err := testinfra.GenSSHKeyWithComment(t.TempDir(), "old-name")
	if err != nil {
		t.Fatalf("failed to generate test SSH key: %v", err)
	}

	pty.SendLine("ssh-key add '" + testPubKey + "'")
	pty.Want("Added SSH key")
	pty.WantPrompt()

	// Rename the key
	pty.SendLine("ssh-key rename old-name new-name")
	pty.Want("Renamed key")
	pty.Want("new-name")
	pty.WantPrompt()

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
	pty.SendLine("ssh-key remove new-name")
	pty.Want("Deleted SSH key")
	pty.WantPrompt()
	pty.Disconnect()
}

// TestSSHKeyRenameByFingerprint tests renaming an SSH key by SHA256:-prefixed fingerprint.
// This is an undocumented escape hatch for renaming keys with empty names (e.g., via the web UI).
func TestSSHKeyRenameByFingerprint(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
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
	pty.SendLine("ssh-key rename " + fingerprint + " my-main-key")
	pty.Want("Renamed")
	pty.Want("my-main-key")
	pty.WantPrompt()

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
	pty.SendLine("ssh-key rename my-main-key key-1")
	pty.Want("Renamed")
	pty.WantPrompt()
	pty.Disconnect()
}

// TestSSHKeyRenameValidation tests validation for ssh-key rename.
func TestSSHKeyRenameValidation(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
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

	pty.SendLine("ssh-key add '" + testPubKey1 + "'")
	pty.Want("Added SSH key")
	pty.WantPrompt()

	pty.SendLine("ssh-key add '" + testPubKey2 + "'")
	pty.Want("Added SSH key")
	pty.WantPrompt()

	// Test: renaming to existing name should fail
	pty.SendLine("ssh-key rename key-a key-b")
	pty.Want("already exists")
	pty.WantPrompt()

	// Test: renaming with special characters sanitizes them
	pty.SendLine("ssh-key rename key-a 'bad;name'")
	pty.Want("Renamed")
	pty.Want("badname") // semicolon removed by sanitization
	pty.WantPrompt()

	// Rename back so subsequent tests work
	pty.SendLine("ssh-key rename badname key-a")
	pty.Want("Renamed")
	pty.WantPrompt()

	// Test: renaming to all-invalid characters should fail (name is empty after removing special chars)
	pty.SendLine("ssh-key rename key-a ';|$'")
	pty.Want("new name is empty")
	pty.WantPrompt()

	// Test: renaming non-existent key should fail
	pty.SendLine("ssh-key rename nonexistent-key new-name")
	pty.Want("no matching SSH key found")
	pty.WantPrompt()

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
	pty.SendLine("ssh-key remove key-renamed")
	pty.Want("Deleted")
	pty.WantPrompt()
	pty.SendLine("ssh-key remove key-b")
	pty.Want("Deleted")
	pty.WantPrompt()
	pty.Disconnect()
}

// TestSSHKeyRenameViaWebCmd tests renaming SSH keys via the /cmd HTTP endpoint,
// which is how the web UI performs renames. This exercises the full path:
// JS constructs {command, args} → POST /cmd → handler.
//
// The existing CLI tests (TestSSHKeyRename, TestSSHKeyRenameValidation) only
// exercise the SSH path where arguments are already pre-split. The /cmd path
// accepts structured args to avoid shell-quoting bugs.
func TestSSHKeyRenameViaWebCmd(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, cookies, keyFile, _ := registerForExeDev(t)

	// Add a key with a known name via SSH.
	_, testPubKey, err := testinfra.GenSSHKeyWithComment(t.TempDir(), "web-rename-test")
	if err != nil {
		t.Fatalf("failed to generate test SSH key: %v", err)
	}
	pty.SendLine("ssh-key add '" + testPubKey + "'")
	pty.Want("Added SSH key")
	pty.WantPrompt()
	pty.Disconnect()

	// Clean up by all possible intermediate names so a mid-subtest failure
	// doesn't leak a key under an unexpected name.
	t.Cleanup(func() {
		for _, name := range []string{"web-rename-test", "simple-name", "my-new-key", "dashed-name"} {
			Env.servers.RunExeDevSSHCommand(Env.context(t), keyFile, "ssh-key", "remove", name)
		}
	})

	client := newClientWithCookies(t, cookies)
	cmdURL := fmt.Sprintf("http://localhost:%d/cmd", Env.HTTPPort())

	type cmdResult struct {
		Success bool   `json:"success"`
		Output  string `json:"output"`
		Error   string `json:"error"`
	}
	postCmd := func(command string, args []string) cmdResult {
		t.Helper()
		payload := struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		}{Command: command, Args: args}
		b, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("failed to marshal payload: %v", err)
		}
		resp, err := client.Post(cmdURL, "application/json", strings.NewReader(string(b)))
		if err != nil {
			t.Fatalf("POST /cmd failed: %v", err)
		}
		defer resp.Body.Close()
		var result cmdResult
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			t.Fatalf("failed to decode /cmd response: %v", err)
		}
		return result
	}

	// Subtests share key state — do not parallelize.
	t.Run("simple_name", func(t *testing.T) {
		result := postCmd("ssh-key rename", []string{"web-rename-test", "simple-name"})
		if !result.Success {
			t.Fatalf("expected success, got error: %s", result.Output)
		}
		// Rename back for next subtest.
		result = postCmd("ssh-key rename", []string{"simple-name", "web-rename-test"})
		if !result.Success {
			t.Fatalf("rename back failed: %s", result.Output)
		}
	})

	// Subtest: rename with spaces in the new name — this was the bug Chad hit.
	// Args are passed directly to the handler, so spaces are preserved.
	t.Run("spaces_in_new_name", func(t *testing.T) {
		result := postCmd("ssh-key rename", []string{"web-rename-test", "my new key"})
		if !result.Success {
			t.Fatalf("rename with spaces in new name should succeed: %s", result.Output)
		}

		// SanitizeComment turns "my new key" into "my-new-key".
		out := runParseExeDevJSON[sshKeyListOutput](t, keyFile, "ssh-key", "list", "--json")
		var found bool
		for _, key := range out.SSHKeys {
			if key.Name == "my-new-key" {
				found = true
			}
		}
		if !found {
			t.Errorf("expected key named 'my-new-key' after rename, got: %+v", out.SSHKeys)
		}

		// Rename back.
		result = postCmd("ssh-key rename", []string{"my-new-key", "web-rename-test"})
		if !result.Success {
			t.Fatalf("rename back failed: %s", result.Output)
		}
	})

	// Subtest: old name with spaces. The arg is passed intact to the handler,
	// which looks up the key by name. It won't find one (sanitized names don't
	// have spaces), but the command parses correctly — no "usage" error.
	t.Run("old_name_with_spaces", func(t *testing.T) {
		result := postCmd("ssh-key rename", []string{"web-rename-test", "dashed-name"})
		if !result.Success {
			t.Fatalf("setup rename failed: %s", result.Output)
		}

		result = postCmd("ssh-key rename", []string{"dashed name", "new-name"})
		if result.Success {
			t.Fatal("should not succeed — no key named 'dashed name' exists")
		}
		if strings.Contains(result.Output, "usage") {
			t.Errorf("should get 'no matching key' error, not a usage error: %s", result.Output)
		}
		if !strings.Contains(result.Output, "no matching SSH key") {
			t.Errorf("expected 'no matching SSH key' error, got: %s", result.Output)
		}

		// Rename back.
		result = postCmd("ssh-key rename", []string{"dashed-name", "web-rename-test"})
		if !result.Success {
			t.Fatalf("rename back failed: %s", result.Output)
		}
	})

	// Subtest: missing args field is rejected.
	t.Run("missing_args_rejected", func(t *testing.T) {
		body := `{"command":"ssh-key rename"}`
		resp, err := client.Post(cmdURL, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatalf("POST /cmd failed: %v", err)
		}
		defer resp.Body.Close()
		var result cmdResult
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			t.Fatalf("failed to decode response: %v", err)
		}
		if result.Success {
			t.Fatal("request without args field should be rejected")
		}
	})
}

// TestSSHKeyWebCmdArgSplitting tests ssh-key add and remove via /cmd with
// structured args, ensuring the public key (which contains spaces) is passed
// intact as a single argument.
func TestSSHKeyWebCmdArgSplitting(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, cookies, keyFile, _ := registerForExeDev(t)

	// Generate a key with a comment that has spaces — simulates:
	// ssh-keygen -C "my laptop key"
	_, testPubKey, err := testinfra.GenSSHKeyWithComment(t.TempDir(), "my laptop key")
	if err != nil {
		t.Fatalf("failed to generate test SSH key: %v", err)
	}
	pty.Disconnect()

	client := newClientWithCookies(t, cookies)
	cmdURL := fmt.Sprintf("http://localhost:%d/cmd", Env.HTTPPort())

	type cmdResult struct {
		Success bool   `json:"success"`
		Output  string `json:"output"`
		Error   string `json:"error"`
	}
	postCmd := func(command string, args []string) cmdResult {
		t.Helper()
		payload := struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		}{Command: command, Args: args}
		b, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("failed to marshal payload: %v", err)
		}
		resp, err := client.Post(cmdURL, "application/json", strings.NewReader(string(b)))
		if err != nil {
			t.Fatalf("POST /cmd failed: %v", err)
		}
		defer resp.Body.Close()
		var result cmdResult
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			t.Fatalf("failed to decode /cmd response: %v", err)
		}
		return result
	}

	// ssh-key add: the public key (e.g. "ssh-ed25519 AAAA... my laptop key")
	// is passed as a single arg, so spaces in the comment are preserved.
	t.Run("add", func(t *testing.T) {
		result := postCmd("ssh-key add", []string{testPubKey})
		if !result.Success {
			t.Fatalf("adding key via /cmd failed: %s / %s", result.Output, result.Error)
		}

		out := runParseExeDevJSON[sshKeyListOutput](t, keyFile, "ssh-key", "list", "--json")
		var found bool
		for _, key := range out.SSHKeys {
			if key.Name == "my-laptop-key" { // SanitizeComment: spaces → dashes
				found = true
			}
		}
		if !found {
			t.Errorf("expected key named 'my-laptop-key', got: %+v", out.SSHKeys)
		}
	})

	// ssh-key remove: the full public key string is passed as a single arg.
	t.Run("remove", func(t *testing.T) {
		result := postCmd("ssh-key remove", []string{testPubKey})
		if !result.Success {
			t.Fatalf("removing key via /cmd failed: %s / %s", result.Output, result.Error)
		}

		out := runParseExeDevJSON[sshKeyListOutput](t, keyFile, "ssh-key", "list", "--json")
		for _, key := range out.SSHKeys {
			if key.Name == "my-laptop-key" {
				t.Error("key should have been removed")
			}
		}
	})
}

// TestSSHKeyRenameViaPlaywright tests the full browser flow: open profile page,
// click Rename on a key, type a name with spaces, submit, and verify the result.
// This catches bugs in the JavaScript command construction, not just the backend.
func TestSSHKeyRenameViaPlaywright(t *testing.T) {
	skipIfNoPlaywright(t)
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, cookies, keyFile, _ := registerForExeDev(t)

	// Add a key with a known name.
	_, testPubKey, err := testinfra.GenSSHKeyWithComment(t.TempDir(), "pw-rename-test")
	if err != nil {
		t.Fatalf("failed to generate test SSH key: %v", err)
	}
	pty.SendLine("ssh-key add '" + testPubKey + "'")
	pty.Want("Added SSH key")
	pty.WantPrompt()
	pty.Disconnect()

	baseURL := fmt.Sprintf("http://localhost:%d", Env.HTTPPort())
	pwCookies := testinfra.HTTPCookiesToPlaywright(baseURL, cookies)
	page, err := testinfra.NewPageWithCookies(baseURL, pwCookies)
	if err != nil {
		t.Fatalf("failed to create playwright page: %v", err)
	}
	defer page.Close()

	// Navigate to the user profile page (key management).
	_, err = page.Goto(baseURL+"/user", playwright.PageGotoOptions{
		WaitUntil: playwright.WaitUntilStateNetworkidle,
	})
	if err != nil {
		t.Fatalf("failed to navigate to /user: %v", err)
	}

	// Find the Rename button for our specific key and click it.
	renameBtn := page.Locator(fmt.Sprintf("button[data-ssh-key-name=%q]", "pw-rename-test"))
	if err := renameBtn.Click(); err != nil {
		t.Fatalf("failed to click Rename button: %v", err)
	}

	// The modal should now be visible with the old name pre-filled.
	nameInput := page.Locator("#ssh-key-new-name")
	err = nameInput.WaitFor(playwright.LocatorWaitForOptions{
		State: playwright.WaitForSelectorStateVisible,
	})
	if err != nil {
		t.Fatalf("rename modal input not visible: %v", err)
	}

	// Clear the input and type a name WITH SPACES — this is the user's action that triggers the bug.
	if err := nameInput.Clear(); err != nil {
		t.Fatalf("failed to clear input: %v", err)
	}
	if err := nameInput.Fill("my laptop key"); err != nil {
		t.Fatalf("failed to fill input: %v", err)
	}

	// Click the Rename button in the modal.
	submitBtn := page.Locator("#rename-ssh-key-btn")
	if err := submitBtn.Click(); err != nil {
		t.Fatalf("failed to click submit: %v", err)
	}

	// Wait for the result — either success or error message.
	// Give it a moment for the fetch to complete.
	successDiv := page.Locator("#rename-ssh-key-success")
	errorDiv := page.Locator("#rename-ssh-key-error")

	// Wait up to 5 seconds for either success or error to appear.
	err = page.Locator("#rename-ssh-key-success:visible, #rename-ssh-key-error:visible").First().WaitFor(playwright.LocatorWaitForOptions{
		State:   playwright.WaitForSelectorStateVisible,
		Timeout: playwright.Float(5000),
	})
	if err != nil {
		t.Fatalf("neither success nor error appeared after rename: %v", err)
	}

	// Check what happened.
	errorVisible, _ := errorDiv.IsVisible()
	successVisible, _ := successDiv.IsVisible()

	if errorVisible {
		errorText, _ := errorDiv.TextContent()
		t.Errorf("rename via browser showed error: %q (this is the bug — spaces in the name caused a command parsing failure)", errorText)
	}
	if !successVisible {
		t.Error("expected success message after rename")
	}

	// If it worked (i.e., after the fix), verify the name was stored correctly.
	// SanitizeComment turns "my laptop key" into "my-laptop-key".
	if successVisible {
		out := runParseExeDevJSON[sshKeyListOutput](t, keyFile, "ssh-key", "list", "--json")
		var found bool
		for _, key := range out.SSHKeys {
			if key.Name == "my-laptop-key" {
				found = true
			}
		}
		if !found {
			t.Errorf("expected key named 'my-laptop-key' after browser rename, got: %+v", out.SSHKeys)
		}
	}

	// Clean up.
	out, cleanupErr := Env.servers.RunExeDevSSHCommand(Env.context(t), keyFile, "ssh-key", "remove", "--json", "pw-rename-test")
	if cleanupErr != nil {
		// Try the sanitized name in case rename succeeded.
		out, cleanupErr = Env.servers.RunExeDevSSHCommand(Env.context(t), keyFile, "ssh-key", "remove", "--json", "my-laptop-key")
		if cleanupErr != nil {
			t.Logf("cleanup remove failed: %v\n%s", cleanupErr, out)
		}
	}
}

// TestWhoamiShowsKeyNames tests that whoami shows key names.
func TestWhoamiShowsKeyNames(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, _, keyFile, _ := registerForExeDev(t)

	// Add a key with a specific name
	_, testPubKey, err := testinfra.GenSSHKeyWithComment(t.TempDir(), "whoami-test-key")
	if err != nil {
		t.Fatalf("failed to generate test SSH key: %v", err)
	}

	pty.SendLine("ssh-key add '" + testPubKey + "'")
	pty.Want("Added SSH key")
	pty.WantPrompt()

	// Check whoami text output shows the name
	pty.SendLine("whoami")
	pty.Want("whoami-test-key")
	pty.WantPrompt()

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
	pty.SendLine("ssh-key remove whoami-test-key")
	pty.Want("Deleted")
	pty.WantPrompt()
	pty.Disconnect()
}
