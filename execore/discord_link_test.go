package execore

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"exe.dev/exedb"
)

func TestVerifyDiscordLinkHMAC(t *testing.T) {
	server := newTestServer(t)
	server.discordLinkSecret = "test-secret-key"

	discordID := "123456789"
	discordUsername := "testuser#1234"
	ts := strconv.FormatInt(time.Now().Unix(), 10)

	// Compute valid HMAC
	data := fmt.Sprintf("%s:%s:%s", discordID, discordUsername, ts)
	mac := hmac.New(sha256.New, []byte(server.discordLinkSecret))
	mac.Write([]byte(data))
	validHMAC := hex.EncodeToString(mac.Sum(nil))

	tests := []struct {
		name            string
		discordID       string
		discordUsername string
		ts              string
		providedHMAC    string
		want            bool
	}{
		{
			name:            "valid HMAC",
			discordID:       discordID,
			discordUsername: discordUsername,
			ts:              ts,
			providedHMAC:    validHMAC,
			want:            true,
		},
		{
			name:            "invalid HMAC",
			discordID:       discordID,
			discordUsername: discordUsername,
			ts:              ts,
			providedHMAC:    "invalidhmac",
			want:            false,
		},
		{
			name:            "expired timestamp",
			discordID:       discordID,
			discordUsername: discordUsername,
			ts:              strconv.FormatInt(time.Now().Unix()-700, 10), // 11+ minutes ago
			providedHMAC:    validHMAC,
			want:            false,
		},
		{
			name:            "invalid timestamp",
			discordID:       discordID,
			discordUsername: discordUsername,
			ts:              "not-a-number",
			providedHMAC:    validHMAC,
			want:            false,
		},
		{
			name:            "wrong discord ID",
			discordID:       "999999999",
			discordUsername: discordUsername,
			ts:              ts,
			providedHMAC:    validHMAC,
			want:            false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := server.verifyDiscordLinkHMAC(tt.discordID, tt.discordUsername, tt.ts, tt.providedHMAC)
			if got != tt.want {
				t.Errorf("verifyDiscordLinkHMAC() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestVerifyDiscordLinkHMACNoSecret(t *testing.T) {
	server := newTestServer(t)
	// discordLinkSecret is empty by default

	got := server.verifyDiscordLinkHMAC("123", "user", "12345", "hmac")
	if got {
		t.Error("verifyDiscordLinkHMAC() should return false when secret is empty")
	}
}

func TestHandleLinkDiscordMissingParams(t *testing.T) {
	server := newTestServer(t)
	server.discordLinkSecret = "test-secret"

	// Create an authenticated user
	ctx := t.Context()
	userID := createTestUser(t, server, "discord-test@example.com")
	cookieValue, err := server.createAuthCookie(ctx, userID, server.env.WebHost)
	if err != nil {
		t.Fatalf("failed to create auth cookie: %v", err)
	}

	tests := []struct {
		name  string
		query string
	}{
		{"missing discord_id", "discord_username=test&ts=123&hmac=abc"},
		{"missing discord_username", "discord_id=123&ts=123&hmac=abc"},
		{"missing ts", "discord_id=123&discord_username=test&hmac=abc"},
		{"missing hmac", "discord_id=123&discord_username=test&ts=123"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/link-discord?"+tt.query, nil)
			req.Host = server.env.WebHost
			req.AddCookie(&http.Cookie{Name: "exe-auth", Value: cookieValue})
			w := httptest.NewRecorder()
			server.handleLinkDiscord(w, req)

			if w.Code != http.StatusBadRequest {
				t.Errorf("got status %d, want %d", w.Code, http.StatusBadRequest)
			}
		})
	}
}

func TestHandleLinkDiscordInvalidHMAC(t *testing.T) {
	server := newTestServer(t)
	server.discordLinkSecret = "test-secret"

	ctx := t.Context()
	userID := createTestUser(t, server, "discord-invalid@example.com")
	cookieValue, err := server.createAuthCookie(ctx, userID, server.env.WebHost)
	if err != nil {
		t.Fatalf("failed to create auth cookie: %v", err)
	}

	ts := strconv.FormatInt(time.Now().Unix(), 10)
	req := httptest.NewRequest("GET", "/link-discord?discord_id=123&discord_username=test&ts="+ts+"&hmac=invalid", nil)
	req.Host = server.env.WebHost
	req.AddCookie(&http.Cookie{Name: "exe-auth", Value: cookieValue})
	w := httptest.NewRecorder()
	server.handleLinkDiscord(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("got status %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestHandleLinkDiscordUnauthenticated(t *testing.T) {
	server := newTestServer(t)
	server.discordLinkSecret = "test-secret"

	req := httptest.NewRequest("GET", "/link-discord?discord_id=123&discord_username=test&ts=123&hmac=abc", nil)
	w := httptest.NewRecorder()
	server.handleLinkDiscord(w, req)

	// Should redirect to auth
	if w.Code != http.StatusTemporaryRedirect {
		t.Errorf("got status %d, want %d", w.Code, http.StatusTemporaryRedirect)
	}
	location := w.Header().Get("Location")
	if location == "" || location[:5] != "/auth" {
		t.Errorf("expected redirect to /auth, got %s", location)
	}
}

func TestHandleLinkDiscordSuccess(t *testing.T) {
	server := newTestServer(t)
	server.discordLinkSecret = "test-secret"

	ctx := t.Context()
	userID := createTestUser(t, server, "discord-success@example.com")
	cookieValue, err := server.createAuthCookie(ctx, userID, server.env.WebHost)
	if err != nil {
		t.Fatalf("failed to create auth cookie: %v", err)
	}

	discordID := "987654321"
	discordUsername := "successuser"
	ts := strconv.FormatInt(time.Now().Unix(), 10)

	// Compute valid HMAC
	data := fmt.Sprintf("%s:%s:%s", discordID, discordUsername, ts)
	mac := hmac.New(sha256.New, []byte(server.discordLinkSecret))
	mac.Write([]byte(data))
	validHMAC := hex.EncodeToString(mac.Sum(nil))

	req := httptest.NewRequest("GET", fmt.Sprintf("/link-discord?discord_id=%s&discord_username=%s&ts=%s&hmac=%s",
		discordID, discordUsername, ts, validHMAC), nil)
	req.Host = server.env.WebHost
	req.AddCookie(&http.Cookie{Name: "exe-auth", Value: cookieValue})
	w := httptest.NewRecorder()
	server.handleLinkDiscord(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("got status %d, want %d: %s", w.Code, http.StatusOK, w.Body.String())
	}

	// Verify the discord_id was stored
	user, err := withRxRes1(server, ctx, (*exedb.Queries).GetUserWithDetails, userID)
	if err != nil {
		t.Fatalf("failed to get user: %v", err)
	}
	if user.DiscordID == nil || *user.DiscordID != discordID {
		t.Errorf("discord_id not stored correctly, got %v, want %s", user.DiscordID, discordID)
	}
}
