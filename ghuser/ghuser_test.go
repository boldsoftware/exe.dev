package ghuser

import (
	"os"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestClient_New(t *testing.T) {
	// Test missing token
	_, err := New("", "whoami.sqlite3")
	if err == nil || err.Error() != "no GitHub token provided" {
		t.Errorf("expected token error, got: %v", err)
	}

	// Test missing DB path
	_, err = New("fake-token", "")
	if err == nil || err.Error() != "no database path provided" {
		t.Errorf("expected DB path error, got: %v", err)
	}

	// Test invalid token (real API call)
	// _, err = New("invalid-token", "whoami.sqlite3")
	// if err == nil {
	// 	t.Error("expected error for invalid token")
	// }

	// Test with real token and DB (if available)
	token := os.Getenv("GITHUB_TOKEN")
	dbPath := os.Getenv("WHOAMI_DB")
	if token != "" && dbPath != "" {
		c, err := New(token, dbPath)
		if err != nil {
			t.Fatalf("failed to create client with valid config: %v", err)
		}
		defer c.Close()

		// Verify database is accessible
		if c.db == nil {
			t.Error("database not opened")
		}
		if c.stmt == nil {
			t.Error("statement not prepared")
		}
	}
}

func TestClient_Info_EndToEnd(t *testing.T) {
	token := os.Getenv("GITHUB_TOKEN")
	dbPath := os.Getenv("WHOAMI_DB")
	if token == "" || dbPath == "" {
		t.Skip("GITHUB_TOKEN and WHOAMI_DB required for end-to-end test")
	}

	// Read the test public key
	pubKeyData, err := os.ReadFile("josharian.pub")
	if err != nil {
		t.Fatalf("failed to read josharian.pub: %v", err)
	}

	// Parse the public key
	pubKey, _, _, _, err := ssh.ParseAuthorizedKey(pubKeyData)
	if err != nil {
		t.Fatalf("failed to parse public key: %v", err)
	}

	// Create client
	c, err := New(token, dbPath)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	defer c.Close()

	// Get info for the key
	info, err := c.InfoKey(t.Context(), pubKey)
	if err != nil {
		t.Fatalf("InfoKey failed: %v", err)
	}

	// Verify results
	if !info.IsGitHubUser {
		t.Error("expected IsGitHubUser to be true for josharian.pub")
	}

	// Check if we got an email (depends on GitHub user privacy settings)
	t.Logf("Email found: %s", info.Email)

	// Check credit status
	t.Logf("Credit OK: %v", info.CreditOK)
}

func TestClient_Info_UnknownKey(t *testing.T) {
	token := os.Getenv("GITHUB_TOKEN")
	dbPath := os.Getenv("WHOAMI_DB")
	if token == "" || dbPath == "" {
		t.Skip("GITHUB_TOKEN and WHOAMI_DB required for test")
	}

	// Generate a random key that won't be in the database (valid base64)
	pubKey, _, _, _, err := ssh.ParseAuthorizedKey([]byte("ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHZ7CHDxXS9230F+FqRLgL9v/bLlHg6cA0w3FgNWALz3 fake@test.com"))
	if err != nil {
		t.Fatalf("failed to parse test key: %v", err)
	}

	// Create client
	c, err := New(token, dbPath)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	defer c.Close()

	// Get info for unknown key
	info, err := c.InfoKey(t.Context(), pubKey)
	if err != nil {
		t.Fatalf("InfoKey failed: %v", err)
	}

	// Should not be recognized as GitHub user
	if info.IsGitHubUser {
		t.Error("expected IsGitHubUser to be false for unknown key")
	}
	if info.Email != "" {
		t.Error("expected no email for unknown key")
	}
	if info.CreditOK {
		t.Error("expected CreditOK to be false for unknown key")
	}
}
