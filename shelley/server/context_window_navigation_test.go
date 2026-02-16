package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestContextWindowSizePreservedOnNavigation tests that context window size
// is correctly returned when loading different conversations
func TestContextWindowSizePreservedOnNavigation(t *testing.T) {
	h := NewTestHarness(t)

	// Create first conversation and get a response
	h.NewConversation("echo: first message", "/tmp")
	resp1 := h.WaitResponse()
	t.Logf("First conversation first response: %q", resp1)

	// Get context window size for first conversation
	firstConvID := h.convID
	firstConvSize := h.GetContextWindowSize()
	t.Logf("First conversation context window size: %d", firstConvSize)
	if firstConvSize == 0 {
		t.Fatal("expected non-zero context window size for first conversation")
	}

	// Create second conversation and get a response
	h.NewConversation("echo: second message with much more text to ensure different context size", "/tmp")
	resp2 := h.WaitResponse()
	t.Logf("Second conversation first response: %q", resp2)

	secondConvID := h.convID
	secondConvSize := h.GetContextWindowSize()
	t.Logf("Second conversation context window size: %d", secondConvSize)
	if secondConvSize == 0 {
		t.Fatal("expected non-zero context window size for second conversation")
	}

	// Now simulate "navigating" back to the first conversation by fetching it via GET
	// This is what the UI does when switching conversations
	req := httptest.NewRequest("GET", "/api/conversation/"+firstConvID, nil)
	w := httptest.NewRecorder()
	h.server.handleGetConversation(w, req, firstConvID)

	if w.Code != http.StatusOK {
		t.Fatalf("GET first conversation returned %d: %s", w.Code, w.Body.String())
	}

	var resp StreamResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	t.Logf("First conversation on navigation: context_window_size=%d, messages=%d",
		resp.ContextWindowSize, len(resp.Messages))

	if resp.ContextWindowSize != firstConvSize {
		t.Errorf("context_window_size mismatch on navigation: got %d, want %d",
			resp.ContextWindowSize, firstConvSize)
	}

	// Now navigate to second conversation
	req = httptest.NewRequest("GET", "/api/conversation/"+secondConvID, nil)
	w = httptest.NewRecorder()
	h.server.handleGetConversation(w, req, secondConvID)

	if w.Code != http.StatusOK {
		t.Fatalf("GET second conversation returned %d: %s", w.Code, w.Body.String())
	}

	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	t.Logf("Second conversation on navigation: context_window_size=%d, messages=%d",
		resp.ContextWindowSize, len(resp.Messages))

	if resp.ContextWindowSize != secondConvSize {
		t.Errorf("context_window_size mismatch on navigation: got %d, want %d",
			resp.ContextWindowSize, secondConvSize)
	}
}
