package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"shelley.exe.dev/db/generated"
)

func TestGetConversationBySlug(t *testing.T) {
	h := NewTestHarness(t)
	defer h.Close()

	// Create a conversation with a slug
	slug := "my-test-slug"
	conv, err := h.db.CreateConversation(t.Context(), &slug, true, nil, nil)
	if err != nil {
		t.Fatalf("Failed to create conversation: %v", err)
	}

	mux := http.NewServeMux()
	h.server.RegisterRoutes(mux)

	// Test successful lookup
	req := httptest.NewRequest("GET", "/api/conversation-by-slug/"+slug, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var result generated.Conversation
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if result.ConversationID != conv.ConversationID {
		t.Errorf("Expected conversation ID %s, got %s", conv.ConversationID, result.ConversationID)
	}

	// Test non-existent slug
	req = httptest.NewRequest("GET", "/api/conversation-by-slug/non-existent-slug", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("Expected status 404, got %d: %s", rec.Code, rec.Body.String())
	}

	// Test empty slug
	req = httptest.NewRequest("GET", "/api/conversation-by-slug/", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("Expected status 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestIsConversationSlugPath(t *testing.T) {
	tests := []struct {
		path   string
		expect bool
	}{
		// Should NOT be treated as slugs
		{"/", false},
		{"/api/conversations", false},
		{"/api/conversation/abc", false},
		{"/debug/llm", false},
		{"/main.js", false},
		{"/styles.css", false},
		{"/index.html", false},
		{"/version", false},
		{"/my-conversation", false}, // not in /c/ namespace
		{"/hello-world", false},
		// Should be treated as slugs (must be under /c/)
		{"/c/my-conversation", true},
		{"/c/hello-world", true},
		{"/c/fix-the-bug", true},
		{"/c/c123abc", true},
	}

	for _, tt := range tests {
		got := isConversationSlugPath(tt.path)
		if got != tt.expect {
			t.Errorf("isConversationSlugPath(%q) = %v, want %v", tt.path, got, tt.expect)
		}
	}
}
