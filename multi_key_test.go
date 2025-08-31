package exe

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"testing"
	"time"

	"exe.dev/sqlite"
	"golang.org/x/crypto/ssh"
)

func TestMultiKeyAuthentication(t *testing.T) {
	t.Parallel()
	server := NewTestServer(t)

	// Generate two different SSH keys
	key1, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pubKey1, err := ssh.NewPublicKey(&key1.PublicKey)
	if err != nil {
		t.Fatal(err)
	}

	key2, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pubKey2, err := ssh.NewPublicKey(&key2.PublicKey)
	if err != nil {
		t.Fatal(err)
	}

	testEmail := "multikey@example.com"

	// Test 1: First key registration creates new user
	t.Run("FirstKeyRegistration", func(t *testing.T) {
		// Create user with first key
		err := server.createUser(t.Context(), string(ssh.MarshalAuthorizedKey(pubKey1)), testEmail)
		if err != nil {
			t.Fatalf("Failed to create user: %v", err)
		}

		// Verify authentication with first key returns verified status
		_, err = server.AuthenticatePublicKey(nil, pubKey1)
		if err != nil {
			t.Fatalf("Authentication failed: %v", err)
		}

		// First key should be verified
		email, verified, err := server.GetEmailBySSHKey(t.Context(), string(ssh.MarshalAuthorizedKey(pubKey1)))
		if err != nil {
			t.Fatalf("Failed to get email by SSH key: %v", err)
		}
		if email != testEmail {
			t.Errorf("Expected email %s from ssh_keys table, got %s", testEmail, email)
		}
		if !verified {
			t.Error("Expected key to be verified")
		}
	})

	// Test 2: Second key for same email requires verification
	t.Run("SecondKeyRequiresVerification", func(t *testing.T) {
		// Try to authenticate with second key (not yet added)
		perms, err := server.AuthenticatePublicKey(nil, pubKey2)
		if err != nil {
			t.Fatalf("Authentication failed: %v", err)
		}

		// Should not be registered yet
		if perms.Extensions["registered"] != "false" {
			t.Errorf("Expected unregistered status for new key")
		}

		// Add second key as unverified (in pending_ssh_keys table)
		token := server.generateToken()
		err = server.db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
			_, err := tx.Exec(`
				INSERT INTO pending_ssh_keys (token, public_key, user_email, expires_at)
				VALUES (?, ?, ?, datetime('now', '+15 minutes'))`,
				token, string(ssh.MarshalAuthorizedKey(pubKey2)), testEmail)
			return err
		})
		if err != nil {
			t.Fatalf("Failed to add unverified SSH key: %v", err)
		}

		// Try authentication again - should be new_device status
		perms, err = server.AuthenticatePublicKey(nil, pubKey2)
		if err != nil {
			t.Fatalf("Authentication failed: %v", err)
		}

		if perms.Extensions["registered"] != "false" {
			t.Errorf("Expected false status for unverified key, got %s", perms.Extensions["registered"])
		}

		if perms.Extensions["email"] != testEmail {
			t.Errorf("Expected email %s, got %s", testEmail, perms.Extensions["email"])
		}
	})

	// Test 3: Verified second key works
	t.Run("VerifiedSecondKey", func(t *testing.T) {
		// Move key from pending to verified (simulate verification)
		// First get the user ID
		var userID string
		err = server.db.Rx(t.Context(), func(ctx context.Context, rx *sqlite.Rx) error {
			return rx.QueryRow("SELECT user_id FROM users WHERE email = ?", testEmail).Scan(&userID)
		})
		if err != nil {
			t.Fatalf("Failed to get user ID: %v", err)
		}

		// Move key from pending_ssh_keys to ssh_keys
		err = server.db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
			if _, err := tx.Exec(`
				INSERT INTO ssh_keys (user_id, public_key)
				VALUES (?, ?)`,
				userID, string(ssh.MarshalAuthorizedKey(pubKey2))); err != nil {
				return err
			}
			// Remove from pending
			if _, err := tx.Exec(`
				DELETE FROM pending_ssh_keys WHERE public_key = ?`,
				string(ssh.MarshalAuthorizedKey(pubKey2))); err != nil {
				return err
			}
			return nil
		})
		if err != nil {
			t.Fatalf("Failed to verify SSH key and remove from pending: %v", err)
		}

		// Create team membership for full authentication
		userID1, err := generateUserID()
		if err != nil {
			t.Fatalf("Failed to generate user ID: %v", err)
		}
		server.db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
			tx.Exec("INSERT OR IGNORE INTO teams (team_name) VALUES ('test-team')")
			tx.Exec(`INSERT OR IGNORE INTO users (user_id, email) VALUES (?, ?)`, userID1, "test1@example.com")
			tx.Exec(`INSERT OR IGNORE INTO team_members
				(user_id, team_name, is_admin)
				VALUES (?, 'test-team', 1)`, userID1)
			return nil
		})

		// Now authentication should succeed
		perms, err := server.AuthenticatePublicKey(nil, pubKey2)
		if err != nil {
			t.Fatalf("Authentication failed: %v", err)
		}

		if perms.Extensions["registered"] != "true" {
			t.Errorf("Expected registered status for verified key, got %s", perms.Extensions["registered"])
		}

		if perms.Extensions["email"] != testEmail {
			t.Errorf("Expected email %s, got %s", testEmail, perms.Extensions["email"])
		}
	})

	// Test 4: Pending key verification flow
	t.Run("PendingKeyVerification", func(t *testing.T) {
		// Generate a third key
		key3, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatal(err)
		}
		pubKey3, err := ssh.NewPublicKey(&key3.PublicKey)
		if err != nil {
			t.Fatal(err)
		}
		publicKey3 := string(ssh.MarshalAuthorizedKey(pubKey3))

		// Create pending key entry
		token := "test-verification-token"
		expires := time.Now().Add(15 * time.Minute)
		err = server.db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
			_, err := tx.Exec(`
				INSERT INTO pending_ssh_keys (token, public_key, user_email, expires_at)
				VALUES (?, ?, ?, ?)`,
				token, publicKey3, testEmail, expires)
			return err
		})
		if err != nil {
			t.Fatalf("Failed to create pending key: %v", err)
		}

		// Verify the pending key exists
		var count int
		err = server.db.Rx(t.Context(), func(ctx context.Context, rx *sqlite.Rx) error {
			return rx.QueryRow(`
				SELECT COUNT(*) FROM pending_ssh_keys WHERE token = ?`,
				token).Scan(&count)
		})
		if err != nil {
			t.Fatalf("Failed to query pending keys: %v", err)
		}
		if count != 1 {
			t.Errorf("Expected 1 pending key, got %d", count)
		}
	})
}

func TestEmailBySSHKey(t *testing.T) {
	t.Parallel()
	server := NewTestServer(t)

	testEmail := "test@example.com"
	testPublicKey := "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQC..."

	// Test non-existent key
	email, verified, err := server.GetEmailBySSHKey(t.Context(), "non-existent")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if email != "" || verified {
		t.Error("Expected empty result for non-existent key")
	}

	// Create user first
	userID, err := generateUserID()
	if err != nil {
		t.Fatalf("Failed to generate user ID: %v", err)
	}
	err = server.db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
		if _, err := tx.Exec(`
			INSERT INTO users (user_id, email)
			VALUES (?, ?)`,
			userID, testEmail); err != nil {
			return err
		}
		// Add verified key
		if _, err := tx.Exec(`
			INSERT INTO ssh_keys (user_id, public_key)
			VALUES (?, ?)`,
			userID, testPublicKey); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Failed to create user and SSH key: %v", err)
	}

	// Test existing verified key
	email, verified, err = server.GetEmailBySSHKey(t.Context(), testPublicKey)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if email != testEmail {
		t.Errorf("Expected email %s, got %s", testEmail, email)
	}
	if !verified {
		t.Error("Expected verified key")
	}

	// Add unverified key (in pending_ssh_keys)
	unverifiedPublicKey := "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQDUnverified..."
	token := server.generateToken()
	err = server.db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := tx.Exec(`
			INSERT INTO pending_ssh_keys (token, public_key, user_email, expires_at)
			VALUES (?, ?, ?, datetime('now', '+15 minutes'))`,
			token, unverifiedPublicKey, testEmail)
		return err
	})
	if err != nil {
		t.Fatalf("Failed to insert unverified SSH key: %v", err)
	}

	// Test unverified key
	email, verified, err = server.GetEmailBySSHKey(t.Context(), unverifiedPublicKey)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if email != testEmail {
		t.Errorf("Expected email %s, got %s", testEmail, email)
	}
	if verified {
		t.Error("Expected unverified key")
	}
}
