package exe

import (
	"crypto/rand"
	"crypto/rsa"
	"os"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestMultiKeyAuthentication(t *testing.T) {
	// Create temporary database file
	tmpDB, err := os.CreateTemp("", "test_multikey_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp db: %v", err)
	}
	defer os.Remove(tmpDB.Name())
	tmpDB.Close()

	server, err := NewServer(":18081", "", ":12223", tmpDB.Name(), "local", "")
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}
	defer server.Stop()

	// Generate two different SSH keys
	key1, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pubKey1, err := ssh.NewPublicKey(&key1.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint1 := server.getPublicKeyFingerprint(pubKey1)

	key2, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pubKey2, err := ssh.NewPublicKey(&key2.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint2 := server.getPublicKeyFingerprint(pubKey2)

	testEmail := "multikey@example.com"

	// Test 1: First key registration creates new user
	t.Run("FirstKeyRegistration", func(t *testing.T) {
		// Create user with first key
		err := server.createUser(fingerprint1, testEmail)
		if err != nil {
			t.Fatalf("Failed to create user: %v", err)
		}

		// Add first key to ssh_keys table
		_, err = server.db.Exec(`
			INSERT INTO ssh_keys (fingerprint, user_email, public_key, verified)
			VALUES (?, ?, ?, 1)`,
			fingerprint1, testEmail, string(ssh.MarshalAuthorizedKey(pubKey1)))
		if err != nil {
			t.Fatalf("Failed to add SSH key: %v", err)
		}

		// Verify authentication with first key returns verified status
		_, err = server.authenticatePublicKey(nil, pubKey1)
		if err != nil {
			t.Fatalf("Authentication failed: %v", err)
		}

		// First key should be verified
		email, verified, err := server.getEmailBySSHKey(fingerprint1)
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
		perms, err := server.authenticatePublicKey(nil, pubKey2)
		if err != nil {
			t.Fatalf("Authentication failed: %v", err)
		}

		// Should not be registered yet
		if perms.Extensions["registered"] != "false" {
			t.Errorf("Expected unregistered status for new key")
		}

		// Add second key as unverified
		_, err = server.db.Exec(`
			INSERT INTO ssh_keys (fingerprint, user_email, public_key, verified)
			VALUES (?, ?, ?, 0)`,
			fingerprint2, testEmail, string(ssh.MarshalAuthorizedKey(pubKey2)))
		if err != nil {
			t.Fatalf("Failed to add unverified SSH key: %v", err)
		}

		// Try authentication again - should be new_device status
		perms, err = server.authenticatePublicKey(nil, pubKey2)
		if err != nil {
			t.Fatalf("Authentication failed: %v", err)
		}

		if perms.Extensions["registered"] != "new_device" {
			t.Errorf("Expected new_device status, got %s", perms.Extensions["registered"])
		}

		if perms.Extensions["email"] != testEmail {
			t.Errorf("Expected email %s, got %s", testEmail, perms.Extensions["email"])
		}
	})

	// Test 3: Verified second key works
	t.Run("VerifiedSecondKey", func(t *testing.T) {
		// Mark second key as verified
		_, err = server.db.Exec(`
			UPDATE ssh_keys SET verified = 1 WHERE fingerprint = ?`,
			fingerprint2)
		if err != nil {
			t.Fatalf("Failed to verify SSH key: %v", err)
		}

		// Create team membership for full authentication
		server.db.Exec("INSERT OR IGNORE INTO teams (name) VALUES ('test-team')")
		server.db.Exec(`INSERT OR IGNORE INTO team_members 
			(user_fingerprint, team_name, is_admin) 
			VALUES (?, 'test-team', 1)`, fingerprint1)

		// Now authentication should succeed
		perms, err := server.authenticatePublicKey(nil, pubKey2)
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
		fingerprint3 := server.getPublicKeyFingerprint(pubKey3)
		publicKey3 := string(ssh.MarshalAuthorizedKey(pubKey3))

		// Create pending key entry
		token := "test-verification-token"
		expires := time.Now().Add(15 * time.Minute)
		_, err = server.db.Exec(`
			INSERT INTO pending_ssh_keys (token, fingerprint, public_key, user_email, expires_at)
			VALUES (?, ?, ?, ?, ?)`,
			token, fingerprint3, publicKey3, testEmail, expires)
		if err != nil {
			t.Fatalf("Failed to create pending key: %v", err)
		}

		// Verify the pending key exists
		var count int
		err = server.db.QueryRow(`
			SELECT COUNT(*) FROM pending_ssh_keys WHERE token = ?`,
			token).Scan(&count)
		if err != nil {
			t.Fatalf("Failed to query pending keys: %v", err)
		}
		if count != 1 {
			t.Errorf("Expected 1 pending key, got %d", count)
		}
	})
}

func TestEmailBySSHKey(t *testing.T) {
	// Create temporary database file
	tmpDB, err := os.CreateTemp("", "test_emailkey_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp db: %v", err)
	}
	defer os.Remove(tmpDB.Name())
	tmpDB.Close()

	server, err := NewServer(":18082", "", ":12224", tmpDB.Name(), "local", "")
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}
	defer server.Stop()

	testEmail := "test@example.com"
	testFingerprint := "test-fingerprint-123"
	testPublicKey := "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQC..."

	// Test non-existent key
	email, verified, err := server.getEmailBySSHKey("non-existent")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if email != "" || verified {
		t.Error("Expected empty result for non-existent key")
	}

	// Add verified key
	_, err = server.db.Exec(`
		INSERT INTO ssh_keys (fingerprint, user_email, public_key, verified)
		VALUES (?, ?, ?, 1)`,
		testFingerprint, testEmail, testPublicKey)
	if err != nil {
		t.Fatalf("Failed to insert SSH key: %v", err)
	}

	// Test existing verified key
	email, verified, err = server.getEmailBySSHKey(testFingerprint)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if email != testEmail {
		t.Errorf("Expected email %s, got %s", testEmail, email)
	}
	if !verified {
		t.Error("Expected verified key")
	}

	// Add unverified key
	unverifiedFingerprint := "unverified-fingerprint-456"
	_, err = server.db.Exec(`
		INSERT INTO ssh_keys (fingerprint, user_email, public_key, verified)
		VALUES (?, ?, ?, 0)`,
		unverifiedFingerprint, testEmail, testPublicKey)
	if err != nil {
		t.Fatalf("Failed to insert unverified SSH key: %v", err)
	}

	// Test unverified key
	email, verified, err = server.getEmailBySSHKey(unverifiedFingerprint)
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

func TestLegacyKeyMigration(t *testing.T) {
	// Create temporary database file
	tmpDB, err := os.CreateTemp("", "test_legacy_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp db: %v", err)
	}
	defer os.Remove(tmpDB.Name())
	tmpDB.Close()

	server, err := NewServer(":18083", "", ":12225", tmpDB.Name(), "local", "")
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}
	defer server.Stop()

	testEmail := "legacy@example.com"
	testFingerprint := "legacy-fingerprint"
	testPublicKey := "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQC..."

	// Create legacy user
	err = server.createUser(testFingerprint, testEmail)
	if err != nil {
		t.Fatalf("Failed to create legacy user: %v", err)
	}

	// Migrate the key
	err = server.migrateLegacyUserKey(testEmail, testFingerprint, testPublicKey)
	if err != nil {
		t.Fatalf("Failed to migrate legacy key: %v", err)
	}

	// Verify the key was migrated
	var count int
	err = server.db.QueryRow(`
		SELECT COUNT(*) FROM ssh_keys 
		WHERE fingerprint = ? AND user_email = ? AND verified = 1`,
		testFingerprint, testEmail).Scan(&count)
	if err != nil {
		t.Fatalf("Failed to query migrated key: %v", err)
	}
	if count != 1 {
		t.Errorf("Expected 1 migrated key, got %d", count)
	}

	// Verify idempotency - migrating again should not error
	err = server.migrateLegacyUserKey(testEmail, testFingerprint, testPublicKey)
	if err != nil {
		t.Fatalf("Migration should be idempotent: %v", err)
	}
}
