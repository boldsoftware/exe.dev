package server

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWhoisIsHuman(t *testing.T) {
	tests := []struct {
		name string
		w    Whois
		want bool
	}{
		{"human user", Whois{LoginName: "alice@example.com"}, true},
		{"tagged node", Whois{LoginName: "alice@example.com", Tags: []string{"tag:ci"}}, false},
		{"tagged node no login", Whois{Tags: []string{"tag:agent"}}, false},
		{"empty whois", Whois{}, false},
		{"display name only", Whois{DisplayName: "Alice"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.w.IsHuman(); got != tc.want {
				t.Errorf("IsHuman() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestRequireHumanTailscaleUser_FailsClosed confirms that when the
// local tailscaled is not reachable (as in unit tests / CI) the
// middleware rejects the request with 403 rather than letting it
// through. This is the critical fail-closed property of the gate.
func TestRequireHumanTailscaleUser_FailsClosed(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	// A handler that would succeed if we ever reached it.
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("inner handler should not have been called")
		w.WriteHeader(http.StatusOK)
	})
	h := RequireHumanTailscaleUser(log)(inner)

	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/anything")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusForbidden)
	}
}

func TestUserFromContext(t *testing.T) {
	ctx := context.Background()
	if _, ok := UserFromContext(ctx); ok {
		t.Error("empty context should have no user")
	}
	want := User{LoginName: "alice@example.com", DisplayName: "Alice"}
	ctx = contextWithUser(ctx, want)
	got, ok := UserFromContext(ctx)
	if !ok {
		t.Fatal("expected user in context")
	}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestUserSlug(t *testing.T) {
	tests := []struct {
		name string
		user User
		want string
	}{
		{name: "login local part", user: User{LoginName: "Philip.Zeyliger@example.com"}, want: "philip-zeyliger"},
		{name: "display name fallback", user: User{DisplayName: "Ada Lovelace"}, want: "ada-lovelace"},
		{name: "trim repeated separators", user: User{LoginName: " alice+ops@example.com "}, want: "alice-ops"},
		{name: "non-ascii letters omitted", user: User{LoginName: "Élodie@example.com"}, want: "lodie"},
		{name: "empty", user: User{}, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := userSlug(tt.user); got != tt.want {
				t.Fatalf("userSlug(%+v) = %q, want %q", tt.user, got, tt.want)
			}
		})
	}
}
