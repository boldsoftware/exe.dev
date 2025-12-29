package execore

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
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
	throttled, msg := s.CheckNewThrottle(ctx, "test@example.com")
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
	throttled, msg := s.CheckNewThrottle(ctx, "anyone@example.com")
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
	throttled, _ = s.CheckNewThrottle(ctx, "anyone@example.com")
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
	throttled, msg := s.CheckNewThrottle(ctx, "user@blocked.com")
	if !throttled {
		t.Error("expected user@blocked.com to be throttled")
	}
	if msg != "Your domain is blocked" {
		t.Errorf("expected 'Your domain is blocked', got %q", msg)
	}

	throttled, _ = s.CheckNewThrottle(ctx, "user@also-blocked.org")
	if !throttled {
		t.Error("expected user@also-blocked.org to be throttled")
	}

	// Check that non-matching emails are not throttled
	throttled, _ = s.CheckNewThrottle(ctx, "user@allowed.com")
	if throttled {
		t.Error("expected user@allowed.com to not be throttled")
	}

	throttled, _ = s.CheckNewThrottle(ctx, "user@blocked.com.other")
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
	throttled, msg := s.CheckNewThrottle(ctx, "test@example.com")
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
	throttled, msg := s.CheckNewThrottle(ctx, "user@test.com")
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
		throttled, _ := s.CheckNewThrottle(ctx, tt.email)
		if throttled != tt.throttled {
			t.Errorf("CheckNewThrottle(%q) = %v, want %v", tt.email, throttled, tt.throttled)
		}
	}
}
