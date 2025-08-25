package exe

import (
	"testing"
)

func TestSSHHostKeyTable(t *testing.T) {
	t.Parallel()
	server := NewTestServer(t)

	// Verify all expected columns exist
	var id int
	var privateKey, publicKey, fingerprint string
	var createdAt, updatedAt string

	err := server.db.QueryRow(`
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
