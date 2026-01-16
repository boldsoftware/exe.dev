package exedb_test

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"

	"exe.dev/exedb"
	"exe.dev/tslog"
	_ "modernc.org/sqlite"
)

// setupTestDB creates a test database with schema applied
func setupTestDB(t *testing.T) (*sql.DB, *exedb.Queries) {
	t.Helper()

	// Create temp db file
	tmpDB, err := os.CreateTemp("", t.Name()+"_*.db")
	if err != nil {
		t.Fatalf("failed to create temp db: %v", err)
	}
	t.Cleanup(func() {
		tmpDB.Close()
		os.Remove(tmpDB.Name())
	})

	// Open SQLite database
	db, err := sql.Open("sqlite", tmpDB.Name())
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	t.Cleanup(func() {
		db.Close()
	})

	// Apply migrations
	if err := exedb.RunMigrations(tslog.Slogger(t), db); err != nil {
		t.Fatalf("failed to run migrations: %v", err)
	}

	queries := exedb.New(db)
	return db, queries
}

// TestInsertSSHKeyForEmailUser tests the InsertSSHKeyForEmailUser function
func TestInsertSSHKeyForEmailUser(t *testing.T) {
	db, queries := setupTestDB(t)
	_ = db // not directly used but needed for setup

	ctx := context.Background()

	// First, create a test user
	userEmail := "test@example.com"
	userID := "usr1234567890123"

	// Insert user directly using raw SQL for setup
	_, err := db.Exec(`INSERT INTO users (user_id, email) VALUES (?, ?)`, userID, userEmail)
	if err != nil {
		t.Fatalf("Failed to create test user: %v", err)
	}

	// Test the function that should work but might have a bug
	publicKey := "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQC7vbqajDhA test@example.com"

	params := exedb.InsertSSHKeyForEmailUserParams{
		Email:     userEmail,
		PublicKey: publicKey,
		Comment:   nil,
	}

	// This should work but currently fails due to parameter mismatch bug
	err = queries.InsertSSHKeyForEmailUser(ctx, params)
	if err != nil {
		t.Logf("InsertSSHKeyForEmailUser failed: %v", err)
		// Check if this is the parameter count error we expect
		if strings.Contains(err.Error(), "missing argument with index 3") ||
			strings.Contains(err.Error(), "expected 3 arguments, got 2") ||
			strings.Contains(err.Error(), "wrong number of arguments") {
			t.Logf("BUG CONFIRMED: The SQL query has 3 parameter placeholders but the generated function only passes 2 arguments")
			t.Logf("SQL query: INSERT INTO ssh_keys (user_id, public_key) VALUES ((SELECT user_id FROM users WHERE email = ?), ?) ON CONFLICT(public_key) DO UPDATE SET user_id = (SELECT user_id FROM users WHERE email = ?)")
			t.Logf("Function call: q.exec(ctx, q.insertSSHKeyForEmailUserStmt, insertSSHKeyForEmailUser, arg.Email, arg.PublicKey)")
			t.Logf("The function should pass: arg.Email, arg.PublicKey, arg.Email (email needs to be passed twice)")
			t.Fatal("Parameter mismatch bug detected - test demonstrates the issue")
		}
		t.Fatalf("Unexpected error (not the parameter mismatch bug): %v", err)
	}

	// If we get here, the function worked - let's verify the key was inserted
	var count int
	err = db.QueryRow(`SELECT COUNT(*) FROM ssh_keys WHERE public_key = ?`, publicKey).Scan(&count)
	if err != nil {
		t.Fatalf("Failed to query ssh_keys: %v", err)
	}

	if count != 1 {
		t.Errorf("Expected 1 ssh key, found %d", count)
	}

	// InsertSSHKeyForEmailUser does NOT have ON CONFLICT handling,
	// so inserting a duplicate public key should fail with a UNIQUE constraint error.
	userEmail2 := "test2@example.com"
	userID2 := "usr2234567890123"

	// Insert second user
	_, err = db.Exec(`INSERT INTO users (user_id, email) VALUES (?, ?)`, userID2, userEmail2)
	if err != nil {
		t.Fatalf("Failed to create second test user: %v", err)
	}

	// Try to insert the same public key for the second user
	// This should fail because public_key is UNIQUE and InsertSSHKeyForEmailUser
	// does not have ON CONFLICT handling
	params2 := exedb.InsertSSHKeyForEmailUserParams{
		Email:     userEmail2,
		PublicKey: publicKey, // Same public key - should fail
		Comment:   nil,
	}

	err = queries.InsertSSHKeyForEmailUser(ctx, params2)
	if err == nil {
		t.Fatal("Expected UNIQUE constraint error when inserting duplicate public key, but got nil")
	}
	if !strings.Contains(err.Error(), "UNIQUE constraint failed") {
		t.Fatalf("Expected UNIQUE constraint error, got: %v", err)
	}

	// Verify the key still belongs to the first user
	var keyOwnerUserID string
	err = db.QueryRow(`SELECT user_id FROM ssh_keys WHERE public_key = ?`, publicKey).Scan(&keyOwnerUserID)
	if err != nil {
		t.Fatalf("Failed to query key owner: %v", err)
	}

	if keyOwnerUserID != userID {
		t.Errorf("Expected key to still belong to user %s, but belongs to %s", userID, keyOwnerUserID)
	}

	// Verify there's still only one entry for this key
	err = db.QueryRow(`SELECT COUNT(*) FROM ssh_keys WHERE public_key = ?`, publicKey).Scan(&count)
	if err != nil {
		t.Fatalf("Failed to count ssh_keys: %v", err)
	}

	if count != 1 {
		t.Errorf("Expected 1 ssh key entry, found %d", count)
	}
}
