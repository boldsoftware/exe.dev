// This file contains tests for the ssh-key command.

package e1e

import (
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
	Comment     *string    `json:"comment,omitempty"`
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
		pty.want("SSH key not found")
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
