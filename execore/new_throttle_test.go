package execore

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"exe.dev/billing/entitlement"
	"exe.dev/exedb"
	"exe.dev/sqlite"
)

func TestNewThrottleConfig(t *testing.T) {
	s := newTestServer(t)

	ctx := context.Background()

	// Initially, no throttle should be set
	config, err := s.GetNewThrottleConfig(ctx)
	if err != nil {
		t.Fatalf("GetNewThrottleConfig: %v", err)
	}
	if config.Enabled {
		t.Error("expected Enabled to be false initially")
	}
	if len(config.EmailPatterns) != 0 {
		t.Errorf("expected no email patterns initially, got %v", config.EmailPatterns)
	}
	if config.Message != "" {
		t.Errorf("expected no message initially, got %q", config.Message)
	}

	// Check that no one is throttled initially
	throttled, msg := s.CheckNewThrottle(ctx, "", "test@example.com")
	if throttled {
		t.Errorf("expected not throttled initially, got throttled with message: %s", msg)
	}
}

func TestCheckNewThrottleGlobalEnabled(t *testing.T) {
	s := newTestServer(t)

	ctx := context.Background()

	// Set global throttle via HTTP POST
	throttleURL := s.httpURL() + "/debug/new-throttle"
	resp, err := http.PostForm(throttleURL, url.Values{
		"enabled":        {"true"},
		"email_patterns": {""},
		"message":        {"Global throttle message"},
	})
	if err != nil {
		t.Fatalf("POST throttle: %v", err)
	}
	resp.Body.Close()

	// Check that user is throttled
	throttled, msg := s.CheckNewThrottle(ctx, "", "anyone@example.com")
	if !throttled {
		t.Error("expected user to be throttled when global throttle is enabled")
	}
	if msg != "Global throttle message" {
		t.Errorf("expected 'Global throttle message', got %q", msg)
	}

	// Disable throttle
	resp, err = http.PostForm(throttleURL, url.Values{
		"enabled":        {""},
		"email_patterns": {""},
		"message":        {""},
	})
	if err != nil {
		t.Fatalf("POST clear throttle: %v", err)
	}
	resp.Body.Close()

	// Check that user is not throttled anymore
	throttled, _ = s.CheckNewThrottle(ctx, "", "anyone@example.com")
	if throttled {
		t.Error("expected user to not be throttled after clearing")
	}
}

func TestCheckNewThrottleEmailPatterns(t *testing.T) {
	s := newTestServer(t)

	ctx := context.Background()

	// Set email pattern throttle
	throttleURL := s.httpURL() + "/debug/new-throttle"
	resp, err := http.PostForm(throttleURL, url.Values{
		"enabled":        {""},
		"email_patterns": {".*@blocked\\.com$\n.*@also-blocked\\.org$"},
		"message":        {"Your domain is blocked"},
	})
	if err != nil {
		t.Fatalf("POST throttle: %v", err)
	}
	resp.Body.Close()

	// Check that matching emails are throttled
	throttled, msg := s.CheckNewThrottle(ctx, "", "user@blocked.com")
	if !throttled {
		t.Error("expected user@blocked.com to be throttled")
	}
	if msg != "Your domain is blocked" {
		t.Errorf("expected 'Your domain is blocked', got %q", msg)
	}

	throttled, _ = s.CheckNewThrottle(ctx, "", "user@also-blocked.org")
	if !throttled {
		t.Error("expected user@also-blocked.org to be throttled")
	}

	// Check that non-matching emails are not throttled
	throttled, _ = s.CheckNewThrottle(ctx, "", "user@allowed.com")
	if throttled {
		t.Error("expected user@allowed.com to not be throttled")
	}

	throttled, _ = s.CheckNewThrottle(ctx, "", "user@blocked.com.other")
	if throttled {
		t.Error("expected user@blocked.com.other to not be throttled (pattern uses $)")
	}
}

func TestCheckNewThrottleDefaultMessage(t *testing.T) {
	s := newTestServer(t)

	ctx := context.Background()

	// Set global throttle with no custom message
	throttleURL := s.httpURL() + "/debug/new-throttle"
	resp, err := http.PostForm(throttleURL, url.Values{
		"enabled":        {"true"},
		"email_patterns": {""},
		"message":        {""},
	})
	if err != nil {
		t.Fatalf("POST throttle: %v", err)
	}
	resp.Body.Close()

	// Check that default message is used
	throttled, msg := s.CheckNewThrottle(ctx, "", "test@example.com")
	if !throttled {
		t.Error("expected user to be throttled")
	}
	if msg != "VM creation is temporarily disabled." {
		t.Errorf("expected default message 'VM creation is temporarily disabled.', got %q", msg)
	}
}

func TestCheckNewThrottleEmailPatternDefaultMessage(t *testing.T) {
	s := newTestServer(t)

	ctx := context.Background()

	// Set email pattern throttle with no custom message
	throttleURL := s.httpURL() + "/debug/new-throttle"
	resp, err := http.PostForm(throttleURL, url.Values{
		"enabled":        {""},
		"email_patterns": {".*@test\\.com$"},
		"message":        {""},
	})
	if err != nil {
		t.Fatalf("POST throttle: %v", err)
	}
	resp.Body.Close()

	// Check that email pattern default message is used
	throttled, msg := s.CheckNewThrottle(ctx, "", "user@test.com")
	if !throttled {
		t.Error("expected user to be throttled")
	}
	if !strings.Contains(msg, "VM creation is not available for your account") {
		t.Errorf("expected default email pattern message, got %q", msg)
	}
}

func TestNewThrottleJSONEndpoint(t *testing.T) {
	s := newTestServer(t)

	// Set some throttle config
	throttleURL := s.httpURL() + "/debug/new-throttle"
	resp, err := http.PostForm(throttleURL, url.Values{
		"enabled":        {"true"},
		"email_patterns": {".*@test\\.com$"},
		"message":        {"Test message"},
	})
	if err != nil {
		t.Fatalf("POST throttle: %v", err)
	}
	resp.Body.Close()

	// Get JSON endpoint
	resp, err = http.Get(throttleURL + "?format=json")
	if err != nil {
		t.Fatalf("GET throttle JSON: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var config NewThrottleConfig
	if err := json.NewDecoder(resp.Body).Decode(&config); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}

	if !config.Enabled {
		t.Error("expected Enabled to be true")
	}
	if len(config.EmailPatterns) != 1 || config.EmailPatterns[0] != ".*@test\\.com$" {
		t.Errorf("expected one email pattern '.*@test\\.com$', got %v", config.EmailPatterns)
	}
	if config.Message != "Test message" {
		t.Errorf("expected message 'Test message', got %q", config.Message)
	}
}

func TestCheckNewThrottleDisposableEmail(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()

	// Disposable emails should be throttled even with no patterns configured
	tests := []struct {
		email     string
		throttled bool
	}{
		{"user@gmail.com", false},
		{"user@outlook.com", false},
		{"user@example.com", false},
		// Known disposable email domains
		{"user@mailinator.com", true},
		{"user@guerrillamail.com", true},
		{"user@yopmail.com", true},
		{"user@10minutemail.com", true},
	}

	for _, tt := range tests {
		throttled, _ := s.CheckNewThrottle(ctx, "", tt.email)
		if throttled != tt.throttled {
			t.Errorf("CheckNewThrottle(%q) = %v, want %v", tt.email, throttled, tt.throttled)
		}
	}
}

func TestCheckNewThrottleStripe(t *testing.T) {
	s := newTestServer(t)
	s.env.SkipBilling = false

	// An email from a disposable name that we normally reject.
	email := "user@mailinator.com"
	publicKey := testSSHPubKey
	user, err := s.createUser(t.Context(), publicKey, email, AllQualityChecks)
	if err != nil {
		t.Fatal(err)
	}

	// Verify that user is rejected without billing information.
	if throttled, _ := s.CheckNewThrottle(t.Context(), user.UserID, email); !throttled {
		t.Error("expected user to be throttled, but was not")
	}

	// Activate billing for the user's canonical account.
	activateUserBilling(t, s, user.UserID)

	throttled, msg := s.CheckNewThrottle(t.Context(), user.UserID, email)
	if throttled {
		t.Errorf("user with activated billing is incorrectly throttled (msg %q)", msg)
	}
}

// TestCheckNewThrottleGrandfathered verifies that a grandfathered user bypasses the
// disposable email throttle because their plan grants vm:create.
func TestCheckNewThrottleGrandfathered(t *testing.T) {
	s := newTestServer(t)
	s.env.SkipBilling = false

	email := "user@mailinator.com" // disposable — normally throttled
	publicKey := testSSHPubKey
	user, err := s.createUser(t.Context(), publicKey, email, AllQualityChecks)
	if err != nil {
		t.Fatal(err)
	}

	// Upgrade the user's account plan to 'grandfathered' so vm:create is granted.
	acct, err := withRxRes1(s, t.Context(), (*exedb.Queries).GetAccountByUserID, user.UserID)
	if err != nil {
		t.Fatalf("GetAccountByUserID: %v", err)
	}
	now := time.Now()
	changedBy := "test:grandfathered"
	err = s.withTx(t.Context(), func(ctx context.Context, q *exedb.Queries) error {
		if err := q.CloseAccountPlan(ctx, exedb.CloseAccountPlanParams{AccountID: acct.ID, EndedAt: &now}); err != nil {
			return err
		}
		return q.InsertAccountPlan(ctx, exedb.InsertAccountPlanParams{
			AccountID: acct.ID,
			PlanID:    string(entitlement.VersionGrandfathered),
			StartedAt: now,
			ChangedBy: &changedBy,
		})
	})
	if err != nil {
		t.Fatalf("Failed to set grandfathered plan: %v", err)
	}

	// Grandfathered user should NOT be throttled — plan grants vm:create.
	throttled, msg := s.CheckNewThrottle(t.Context(), user.UserID, email)
	if throttled {
		t.Errorf("grandfathered user incorrectly throttled (msg %q)", msg)
	}
}

// TestCheckNewThrottleFreeExemption verifies that a user on the 'friend' plan
// bypasses the disposable email throttle because their plan grants vm:create.
func TestCheckNewThrottleFreeExemption(t *testing.T) {
	s := newTestServer(t)
	s.env.SkipBilling = false

	email := "user@guerrillamail.com" // disposable — normally throttled
	publicKey := testSSHPubKey
	user, err := s.createUser(t.Context(), publicKey, email, AllQualityChecks)
	if err != nil {
		t.Fatal(err)
	}

	// Upgrade the user's account plan to 'friend' so vm:create is granted.
	acct, err := withRxRes1(s, t.Context(), (*exedb.Queries).GetAccountByUserID, user.UserID)
	if err != nil {
		t.Fatalf("GetAccountByUserID: %v", err)
	}
	now := time.Now()
	changedBy := "test:friend"
	err = s.withTx(t.Context(), func(ctx context.Context, q *exedb.Queries) error {
		if err := q.CloseAccountPlan(ctx, exedb.CloseAccountPlanParams{AccountID: acct.ID, EndedAt: &now}); err != nil {
			return err
		}
		return q.InsertAccountPlan(ctx, exedb.InsertAccountPlanParams{
			AccountID: acct.ID,
			PlanID:    string(entitlement.VersionFriend),
			StartedAt: now,
			ChangedBy: &changedBy,
		})
	})
	if err != nil {
		t.Fatalf("Failed to set friend plan: %v", err)
	}

	// Friend user should NOT be throttled — plan grants vm:create.
	throttled, msg := s.CheckNewThrottle(t.Context(), user.UserID, email)
	if throttled {
		t.Errorf("friend user incorrectly throttled (msg %q)", msg)
	}
}

// TestCheckNewThrottleBasicUser verifies that a Basic plan user (no billing, created
// after cutoff) IS still throttled by disposable email check.
func TestCheckNewThrottleBasicUser(t *testing.T) {
	s := newTestServer(t)
	s.env.SkipBilling = false

	email := "user@yopmail.com" // disposable
	publicKey := testSSHPubKey
	user, err := s.createUser(t.Context(), publicKey, email, AllQualityChecks)
	if err != nil {
		t.Fatal(err)
	}

	// Set created_at to after billing-required date so user is Basic plan.
	err = s.db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := tx.Conn().ExecContext(ctx, `UPDATE users SET created_at = '2026-02-01 00:00:00' WHERE user_id = ?`, user.UserID)
		return err
	})
	if err != nil {
		t.Fatalf("Failed to update created_at: %v", err)
	}

	// Basic user with disposable email should still be throttled.
	throttled, _ := s.CheckNewThrottle(t.Context(), user.UserID, email)
	if !throttled {
		t.Error("expected Basic user with disposable email to be throttled")
	}
}
