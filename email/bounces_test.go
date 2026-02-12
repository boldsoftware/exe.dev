package email

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"exe.dev/tslog"
)

// mockBounceStore is a test implementation of BounceStore.
type mockBounceStore struct {
	mu           sync.Mutex
	lastPollTime time.Time
	bounces      []BounceRecord
}

func (m *mockBounceStore) GetLastBouncesPollTime(ctx context.Context) (time.Time, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastPollTime, nil
}

func (m *mockBounceStore) SetLastBouncesPollTime(ctx context.Context, t time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastPollTime = t
	return nil
}

func (m *mockBounceStore) StoreBounce(ctx context.Context, bounce BounceRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.bounces = append(m.bounces, bounce)
	return nil
}

func (m *mockBounceStore) getBounces() []BounceRecord {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]BounceRecord{}, m.bounces...)
}

func TestBouncePoller_PollsAndStoresBounces(t *testing.T) {
	// Set up a mock Postmark server
	bounceTime := time.Now().Add(-1 * time.Hour)
	mockBounces := []map[string]any{
		{
			"ID":            1,
			"Type":          "HardBounce",
			"TypeCode":      1,
			"Name":          "Hard bounce",
			"Tag":           "",
			"MessageID":     "abc123",
			"Description":   "The email account does not exist.",
			"Details":       "smtp;550 5.1.1 The email account does not exist.",
			"Email":         "bounced@example.com",
			"BouncedAt":     bounceTime.Format(time.RFC3339),
			"DumpAvailable": true,
			"Inactive":      true,
			"CanActivate":   true,
			"Subject":       "Test Subject",
		},
		{
			"ID":            2,
			"Type":          "SoftBounce",
			"TypeCode":      2,
			"Name":          "Soft bounce",
			"Email":         "softbounce@example.com",
			"BouncedAt":     bounceTime.Add(1 * time.Minute).Format(time.RFC3339),
			"Inactive":      false, // Not inactive, should be skipped
			"Description":   "Mailbox full",
			"DumpAvailable": false,
			"CanActivate":   false,
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bounces" {
			t.Errorf("unexpected path: %s", r.URL.Path)
			http.Error(w, "not found", http.StatusNotFound)
			return
		}

		// Check authorization header
		if r.Header.Get("X-Postmark-Server-Token") != "test-api-key" {
			t.Errorf("missing or invalid auth header")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		resp := map[string]any{
			"TotalCount": len(mockBounces),
			"Bounces":    mockBounces,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	// Create store and poller
	store := &mockBounceStore{}
	logger := tslog.Slogger(t)

	// Create poller with a very long interval (we'll call pollOnce directly)
	poller := NewPostmarkBouncePoller("test-api-key", store, logger, 24*time.Hour)

	// Override the client's base URL to point to our mock server
	poller.client.BaseURL = server.URL

	// Run a single poll
	poller.pollOnce()

	// Verify results
	bounces := store.getBounces()
	if len(bounces) != 1 {
		t.Errorf("expected 1 bounce (inactive only), got %d", len(bounces))
	}

	if len(bounces) > 0 {
		if bounces[0].Email != "bounced@example.com" {
			t.Errorf("expected email 'bounced@example.com', got '%s'", bounces[0].Email)
		}
		if bounces[0].Reason != "HardBounce: The email account does not exist." {
			t.Errorf("unexpected reason: %s", bounces[0].Reason)
		}
	}

	// Verify poll time was updated
	if store.lastPollTime.IsZero() {
		t.Error("last poll time should have been updated")
	}
}

func TestBouncePoller_SkipsAlreadyProcessedBounces(t *testing.T) {
	// Set up store with a recent poll time
	lastPoll := time.Now().Add(-30 * time.Minute)
	oldBounceTime := lastPoll.Add(-1 * time.Hour)   // Before last poll
	newBounceTime := lastPoll.Add(10 * time.Minute) // After last poll

	mockBounces := []map[string]any{
		{
			"ID":          1,
			"Type":        "HardBounce",
			"Email":       "old@example.com",
			"BouncedAt":   oldBounceTime.Format(time.RFC3339),
			"Inactive":    true,
			"Description": "Old bounce",
		},
		{
			"ID":          2,
			"Type":        "HardBounce",
			"Email":       "new@example.com",
			"BouncedAt":   newBounceTime.Format(time.RFC3339),
			"Inactive":    true,
			"Description": "New bounce",
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"TotalCount": len(mockBounces),
			"Bounces":    mockBounces,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	store := &mockBounceStore{lastPollTime: lastPoll}
	logger := tslog.Slogger(t)

	poller := NewPostmarkBouncePoller("test-api-key", store, logger, 24*time.Hour)
	poller.client.BaseURL = server.URL

	poller.pollOnce()

	// Should only have the new bounce
	bounces := store.getBounces()
	if len(bounces) != 1 {
		t.Errorf("expected 1 bounce (new only), got %d", len(bounces))
	}

	if len(bounces) > 0 && bounces[0].Email != "new@example.com" {
		t.Errorf("expected email 'new@example.com', got '%s'", bounces[0].Email)
	}
}
