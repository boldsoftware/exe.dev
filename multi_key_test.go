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

	server, err := NewServer(":18081", "", ":12223", ":0", tmpDB.Name(), "local", []string{""})
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
		err := server.createUser(string(ssh.MarshalAuthorizedKey(pubKey1)), testEmail)
		if err != nil {
			t.Fatalf("Failed to create user: %v", err)
		}

		// Verify authentication with first key returns verified status
		_, err = server.AuthenticatePublicKey(nil, pubKey1)
		if err != nil {
			t.Fatalf("Authentication failed: %v", err)
		}

		// First key should be verified
		email, verified, err := server.GetEmailBySSHKey(string(ssh.MarshalAuthorizedKey(pubKey1)))
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

		// Add second key as unverified
		_, err = server.db.Exec(`
			INSERT INTO ssh_keys (user_id, public_key, verified)
			VALUES ((SELECT user_id FROM users WHERE email = ?), ?, 0)`,
			testEmail, string(ssh.MarshalAuthorizedKey(pubKey2)))
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
		// Mark second key as verified
		_, err = server.db.Exec(`
			UPDATE ssh_keys SET verified = 1 WHERE public_key = ?`,
			string(ssh.MarshalAuthorizedKey(pubKey2)))
		if err != nil {
			t.Fatalf("Failed to verify SSH key: %v", err)
		}

		// Create team membership for full authentication
		server.db.Exec("INSERT OR IGNORE INTO teams (team_name) VALUES ('test-team')")
		// Get or create user and add to team
		userID1, err := generateUserID()
		if err != nil {
			t.Fatalf("Failed to generate user ID: %v", err)
		}
		server.db.Exec(`INSERT OR IGNORE INTO users (user_id, email) VALUES (?, ?)`, userID1, "test1@example.com")
		server.db.Exec(`INSERT OR IGNORE INTO team_members 
			(user_id, team_name, is_admin) 
			VALUES (?, 'test-team', 1)`, userID1)

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
		_, err = server.db.Exec(`
			INSERT INTO pending_ssh_keys (token, public_key, user_email, expires_at)
			VALUES (?, ?, ?, ?)`,
			token, publicKey3, testEmail, expires)
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

	server, err := NewServer(":18082", "", ":12224", ":0", tmpDB.Name(), "local", []string{""})
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}
	defer server.Stop()

	testEmail := "test@example.com"
	testPublicKey := "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQC..."

	// Test non-existent key
	email, verified, err := server.GetEmailBySSHKey("non-existent")
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
	_, err = server.db.Exec(`
		INSERT INTO users (user_id, email)
		VALUES (?, ?)`,
		userID, testEmail)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	// Add verified key
	_, err = server.db.Exec(`
		INSERT INTO ssh_keys (user_id, public_key, verified)
		VALUES (?, ?, 1)`,
		userID, testPublicKey)
	if err != nil {
		t.Fatalf("Failed to insert SSH key: %v", err)
	}

	// Test existing verified key
	email, verified, err = server.GetEmailBySSHKey(testPublicKey)
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
	unverifiedPublicKey := "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQDUnverified..."
	_, err = server.db.Exec(`
		INSERT INTO ssh_keys (user_id, public_key, verified)
		VALUES (?, ?, 0)`,
		userID, unverifiedPublicKey)
	if err != nil {
		t.Fatalf("Failed to insert unverified SSH key: %v", err)
	}

	// Test unverified key
	email, verified, err = server.GetEmailBySSHKey(unverifiedPublicKey)
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
