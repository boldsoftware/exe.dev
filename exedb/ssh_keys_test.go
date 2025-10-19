package exedb_test

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"

	"exe.dev/exedb"
	"exe.dev/testutil"
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
	if err := exedb.RunMigrations(testutil.Slogger(t), db); err != nil {
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

	// Test the ON CONFLICT behavior by inserting the same key again
	// but for a different user
	userEmail2 := "test2@example.com"
	userID2 := "usr2234567890123"

	// Insert second user
	_, err = db.Exec(`INSERT INTO users (user_id, email) VALUES (?, ?)`, userID2, userEmail2)
	if err != nil {
		t.Fatalf("Failed to create second test user: %v", err)
	}

	// Try to insert the same public key for the second user
	// This should trigger the ON CONFLICT clause
	params2 := exedb.InsertSSHKeyForEmailUserParams{
		Email:     userEmail2,
		PublicKey: publicKey, // Same public key
	}

	err = queries.InsertSSHKeyForEmailUser(ctx, params2)
	if err != nil {
		t.Fatalf("Second InsertSSHKeyForEmailUser failed: %v", err)
	}

	// Verify the key now belongs to the second user (updated via ON CONFLICT)
	var keyOwnerUserID string
	err = db.QueryRow(`SELECT user_id FROM ssh_keys WHERE public_key = ?`, publicKey).Scan(&keyOwnerUserID)
	if err != nil {
		t.Fatalf("Failed to query key owner: %v", err)
	}

	if keyOwnerUserID != userID2 {
		t.Errorf("Expected key to belong to user %s, but belongs to %s", userID2, keyOwnerUserID)
	}

	// Verify there's still only one entry for this key
	err = db.QueryRow(`SELECT COUNT(*) FROM ssh_keys WHERE public_key = ?`, publicKey).Scan(&count)
	if err != nil {
		t.Fatalf("Failed to count ssh_keys: %v", err)
	}

	if count != 1 {
		t.Errorf("Expected 1 ssh key entry after conflict resolution, found %d", count)
	}
}
