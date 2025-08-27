package exe

import (
	"os"
	"testing"
)

func TestSSHHostKeyPersistence(t *testing.T) {
	t.Parallel()

	// Create temporary database file
	tmpDB, err := os.CreateTemp("", "test_hostkey_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp db: %v", err)
	}
	defer os.Remove(tmpDB.Name())
	tmpDB.Close()

	// First server instance - should generate and store a new key
	server1, err := NewServer(":18090", "", ":12230", ":0", tmpDB.Name(), "local", []string{""})
	if err != nil {
		t.Fatalf("Failed to create first server: %v", err)
	}

	// Get the fingerprint of the first server's host key
	var fingerprint1 string
	err = server1.db.QueryRow(`SELECT fingerprint FROM ssh_host_key WHERE id = 1`).Scan(&fingerprint1)
	if err != nil {
		t.Fatalf("Failed to retrieve host key fingerprint from database: %v", err)
	}

	// Verify we have a fingerprint
	if fingerprint1 == "" {
		t.Error("Expected non-empty fingerprint for first server")
	}

	t.Logf("First server host key fingerprint: %s", fingerprint1)

	// Stop the first server
	server1.Stop()

	// Second server instance - should load the existing key
	server2, err := NewServer(":18091", "", ":12231", ":0", tmpDB.Name(), "local", []string{""})
	if err != nil {
		t.Fatalf("Failed to create second server: %v", err)
	}
	defer server2.Stop()

	// Get the fingerprint of the second server's host key
	var fingerprint2 string
	err = server2.db.QueryRow(`SELECT fingerprint FROM ssh_host_key WHERE id = 1`).Scan(&fingerprint2)
	if err != nil {
		t.Fatalf("Failed to retrieve host key fingerprint from database: %v", err)
	}

	// Verify the fingerprints match
	if fingerprint1 != fingerprint2 {
		t.Errorf("Host key fingerprints don't match: first=%s, second=%s", fingerprint1, fingerprint2)
	} else {
		t.Logf("Host key persisted successfully: %s", fingerprint2)
	}

	// Verify there's still only one row in the table
	var count int
	err = server2.db.QueryRow(`SELECT COUNT(*) FROM ssh_host_key`).Scan(&count)
	if err != nil {
		t.Fatalf("Failed to count host keys: %v", err)
	}

	if count != 1 {
		t.Errorf("Expected exactly 1 host key in database, got %d", count)
	}
}

func TestSSHHostKeyTable(t *testing.T) {
	t.Parallel()

	// Create temporary database file
	tmpDB, err := os.CreateTemp("", "test_hostkey_table_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp db: %v", err)
	}
	defer os.Remove(tmpDB.Name())
	tmpDB.Close()

	server, err := NewServer(":18092", "", ":12232", ":0", tmpDB.Name(), "local", []string{""})
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}
	defer server.Stop()

	// Verify all expected columns exist
	var id int
	var privateKey, publicKey, fingerprint string
	var createdAt, updatedAt string

	err = server.db.QueryRow(`
		SELECT id, private_key, public_key, fingerprint, created_at, updated_at
		FROM ssh_host_key
		WHERE id = 1`).Scan(&id, &privateKey, &publicKey, &fingerprint, &createdAt, &updatedAt)
	if err != nil {
		t.Fatalf("Failed to query host key table: %v", err)
	}

	// Verify data integrity
	if id != 1 {
		t.Errorf("Expected id=1, got %d", id)
	}

	if privateKey == "" {
		t.Error("Private key should not be empty")
	}

	if publicKey == "" {
		t.Error("Public key should not be empty")
	}

	if fingerprint == "" {
		t.Error("Fingerprint should not be empty")
	}

	if createdAt == "" {
		t.Error("Created timestamp should not be empty")
	}

	if updatedAt == "" {
		t.Error("Updated timestamp should not be empty")
	}

	// Verify the CHECK constraint works (can't insert a second row)
	_, err = server.db.Exec(`
		INSERT INTO ssh_host_key (id, private_key, public_key, fingerprint)
		VALUES (2, 'test', 'test', 'test')`)

	if err == nil {
		t.Error("Should not be able to insert a second row due to CHECK constraint")
	}
}
