package execore

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"exe.dev/exedb"
)

func TestPasskeyRegisterStartRequiresAuth(t *testing.T) {
	t.Parallel()

	s := newUnstartedServer(t)

	req := httptest.NewRequest(http.MethodPost, "/passkey/register/start", nil)
	rr := httptest.NewRecorder()

	s.handlePasskeyRoutes(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, rr.Code)
	}
}

func TestPasskeyRegisterStartReturnsOptions(t *testing.T) {
	t.Parallel()

	s := newUnstartedServer(t)
	s.startAndAwaitReady()
	ctx := context.Background()

	// Create a test user
	userID := createTestUser(t, s, "test@example.com")

	// Create an auth cookie for the user
	cookieValue, err := s.createAuthCookie(ctx, userID, "localhost")
	if err != nil {
		t.Fatalf("failed to create auth cookie: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/passkey/register/start", nil)
	req.Host = "localhost"
	req.AddCookie(&http.Cookie{Name: "exe-auth", Value: cookieValue})
	rr := httptest.NewRecorder()

	s.handlePasskeyRoutes(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, rr.Code, rr.Body.String())
	}

	// Verify response contains expected WebAuthn options
	var options map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &options); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	publicKey, ok := options["publicKey"].(map[string]interface{})
	if !ok {
		t.Fatal("response missing publicKey")
	}

	if _, ok := publicKey["challenge"]; !ok {
		t.Fatal("response missing challenge")
	}

	if _, ok := publicKey["rp"]; !ok {
		t.Fatal("response missing rp (relying party)")
	}

	if _, ok := publicKey["user"]; !ok {
		t.Fatal("response missing user")
	}
}

func TestPasskeyLoginStartReturnsOptions(t *testing.T) {
	t.Parallel()

	s := newUnstartedServer(t)

	req := httptest.NewRequest(http.MethodPost, "/passkey/login/start", nil)
	rr := httptest.NewRecorder()

	s.handlePasskeyRoutes(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, rr.Code, rr.Body.String())
	}

	// Verify response contains expected WebAuthn options
	var options map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &options); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	publicKey, ok := options["publicKey"].(map[string]interface{})
	if !ok {
		t.Fatal("response missing publicKey")
	}

	if _, ok := publicKey["challenge"]; !ok {
		t.Fatal("response missing challenge")
	}

	if _, ok := publicKey["rpId"]; !ok {
		t.Fatal("response missing rpId")
	}
}

func TestPasskeyDeleteRequiresAuth(t *testing.T) {
	t.Parallel()

	s := newUnstartedServer(t)

	form := url.Values{}
	form.Set("id", "1")

	req := httptest.NewRequest(http.MethodPost, "/passkey/delete", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()

	s.handlePasskeyRoutes(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, rr.Code)
	}
}

func TestPasskeyDeleteRemovesPasskey(t *testing.T) {
	t.Parallel()

	s := newUnstartedServer(t)
	s.startAndAwaitReady()
	ctx := context.Background()

	// Create a test user
	userID := createTestUser(t, s, "delete-test@example.com")

	// Create an auth cookie for the user
	cookieValue, err := s.createAuthCookie(ctx, userID, "localhost")
	if err != nil {
		t.Fatalf("failed to create auth cookie: %v", err)
	}

	// Insert a test passkey directly into the database
	err = s.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
		return queries.InsertPasskey(ctx, exedb.InsertPasskeyParams{
			UserID:       userID,
			CredentialID: []byte("test-credential-id"),
			PublicKey:    []byte("test-public-key"),
			SignCount:    0,
			Aaguid:       []byte("test-aaguid"),
			Name:         "Test Passkey",
			Flags:        0,
		})
	})
	if err != nil {
		t.Fatalf("failed to insert test passkey: %v", err)
	}

	// Get the passkey ID
	passkeys, err := s.getPasskeysForUser(ctx, userID)
	if err != nil {
		t.Fatalf("failed to get passkeys: %v", err)
	}
	if len(passkeys) != 1 {
		t.Fatalf("expected 1 passkey, got %d", len(passkeys))
	}

	// Delete the passkey
	form := url.Values{}
	form.Set("id", "1") // First passkey should have ID 1

	req := httptest.NewRequest(http.MethodPost, "/passkey/delete", strings.NewReader(form.Encode()))
	req.Host = "localhost"
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "exe-auth", Value: cookieValue})
	rr := httptest.NewRecorder()

	s.handlePasskeyRoutes(rr, req)

	// Should redirect to /user on success
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("expected status %d, got %d: %s", http.StatusSeeOther, rr.Code, rr.Body.String())
	}

	// Verify passkey was deleted
	passkeysAfter, err := s.getPasskeysForUser(ctx, userID)
	if err != nil {
		t.Fatalf("failed to get passkeys after delete: %v", err)
	}
	if len(passkeysAfter) != 0 {
		t.Fatalf("expected 0 passkeys after delete, got %d", len(passkeysAfter))
	}
}

func TestPasskeyChallengeExpiration(t *testing.T) {
	t.Parallel()

	s := newUnstartedServer(t)
	ctx := context.Background()

	// Insert an expired challenge
	err := s.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
		return queries.InsertPasskeyChallenge(ctx, exedb.InsertPasskeyChallengeParams{
			Challenge:   "expired-test-challenge",
			SessionData: []byte(`{"challenge":"expired-test-challenge"}`),
			UserID:      nil,
			ExpiresAt:   time.Now().Add(-1 * time.Hour), // Already expired
		})
	})
	if err != nil {
		t.Fatalf("failed to insert expired challenge: %v", err)
	}

	// Clean up expired challenges
	err = s.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
		return queries.CleanupExpiredPasskeyChallenges(ctx, time.Now())
	})
	if err != nil {
		t.Fatalf("failed to cleanup expired challenges: %v", err)
	}

	// Try to get the challenge - should not find it (either cleaned up or expired)
	_, err = withRxRes1(s, ctx, (*exedb.Queries).GetPasskeyChallenge, "expired-test-challenge")
	if err == nil {
		t.Fatal("expected error getting expired challenge, got nil")
	}
}

func TestPasskeyRoutesMethodNotAllowed(t *testing.T) {
	t.Parallel()

	s := newUnstartedServer(t)
	s.startAndAwaitReady()
	ctx := context.Background()

	// Create a test user and auth cookie
	userID := createTestUser(t, s, "method-test@example.com")
	cookieValue, _ := s.createAuthCookie(ctx, userID, "localhost")

	// Tests for authenticated routes (include auth cookie)
	authedTests := []struct {
		path   string
		method string
	}{
		{"/passkey/register/start", http.MethodGet},
		{"/passkey/register/finish", http.MethodGet},
		{"/passkey/delete", http.MethodGet},
	}

	for _, tc := range authedTests {
		t.Run(tc.path+"_"+tc.method, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			req.Host = "localhost"
			req.AddCookie(&http.Cookie{Name: "exe-auth", Value: cookieValue})
			rr := httptest.NewRecorder()

			s.handlePasskeyRoutes(rr, req)

			if rr.Code != http.StatusMethodNotAllowed {
				t.Fatalf("expected status %d for %s %s, got %d", http.StatusMethodNotAllowed, tc.method, tc.path, rr.Code)
			}
		})
	}

	// Tests for unauthenticated login routes
	loginTests := []struct {
		path   string
		method string
	}{
		{"/passkey/login/start", http.MethodGet},
		{"/passkey/login/finish", http.MethodGet},
	}

	for _, tc := range loginTests {
		t.Run(tc.path+"_"+tc.method, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			rr := httptest.NewRecorder()

			s.handlePasskeyRoutes(rr, req)

			if rr.Code != http.StatusMethodNotAllowed {
				t.Fatalf("expected status %d for %s %s, got %d", http.StatusMethodNotAllowed, tc.method, tc.path, rr.Code)
			}
		})
	}
}

func TestPasskeyLoginFinishWithInvalidCredential(t *testing.T) {
	t.Parallel()

	s := newUnstartedServer(t)

	// Send an invalid credential response
	invalidResponse := map[string]interface{}{
		"id":    "invalid-id",
		"rawId": "aW52YWxpZC1pZA", // base64url of "invalid-id"
		"type":  "public-key",
		"response": map[string]string{
			"clientDataJSON":    "e30", // base64url of "{}"
			"authenticatorData": "dGVzdA",
			"signature":         "dGVzdA",
		},
	}

	body, _ := json.Marshal(invalidResponse)
	req := httptest.NewRequest(http.MethodPost, "/passkey/login/finish", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	s.handlePasskeyRoutes(rr, req)

	// Should fail with bad request or unauthorized
	if rr.Code == http.StatusOK {
		t.Fatal("expected non-OK status for invalid credential")
	}
}

func TestGetPasskeysForUser(t *testing.T) {
	t.Parallel()

	s := newUnstartedServer(t)
	ctx := context.Background()

	// Create a test user
	userID := createTestUser(t, s, "passkeys-list@example.com")

	// Initially should have no passkeys
	passkeys, err := s.getPasskeysForUser(ctx, userID)
	if err != nil {
		t.Fatalf("failed to get passkeys: %v", err)
	}
	if len(passkeys) != 0 {
		t.Fatalf("expected 0 passkeys initially, got %d", len(passkeys))
	}

	// Insert a passkey
	err = s.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
		return queries.InsertPasskey(ctx, exedb.InsertPasskeyParams{
			UserID:       userID,
			CredentialID: []byte("credential-1"),
			PublicKey:    []byte("public-key-1"),
			SignCount:    0,
			Aaguid:       []byte("aaguid-1"),
			Name:         "MacBook Pro",
			Flags:        0,
		})
	})
	if err != nil {
		t.Fatalf("failed to insert passkey: %v", err)
	}

	// Should now have one passkey
	passkeys, err = s.getPasskeysForUser(ctx, userID)
	if err != nil {
		t.Fatalf("failed to get passkeys after insert: %v", err)
	}
	if len(passkeys) != 1 {
		t.Fatalf("expected 1 passkey, got %d", len(passkeys))
	}
	if passkeys[0].Name != "MacBook Pro" {
		t.Fatalf("expected passkey name 'MacBook Pro', got '%s'", passkeys[0].Name)
	}
	if passkeys[0].LastUsedAt != "Never" {
		t.Fatalf("expected LastUsedAt 'Never', got '%s'", passkeys[0].LastUsedAt)
	}
}

func TestWebAuthnConfiguration(t *testing.T) {
	t.Parallel()

	s := newUnstartedServer(t)

	wa, err := s.getWebAuthn()
	if err != nil {
		t.Fatalf("failed to get WebAuthn: %v", err)
	}

	// Verify configuration
	if wa.Config.RPDisplayName != "EXE.DEV" {
		t.Fatalf("expected RPDisplayName 'EXE.DEV', got '%s'", wa.Config.RPDisplayName)
	}

	if wa.Config.RPID != s.env.WebHost {
		t.Fatalf("expected RPID '%s', got '%s'", s.env.WebHost, wa.Config.RPID)
	}
}

// createTestUser creates a test user and returns their ID
func createTestUser(t *testing.T, s *Server, email string) string {
	t.Helper()
	ctx := context.Background()

	var userID string
	err := s.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
		var err error
		userID, err = s.createUserRecord(ctx, queries, email, false)
		return err
	})
	if err != nil {
		t.Fatalf("failed to create test user: %v", err)
	}
	return userID
}
