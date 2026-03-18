package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"exe.dev/exe-ops/apitype"
)

func TestChatEndpointsWithoutProvider(t *testing.T) {
	db, err := OpenDB(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	store := NewStore(db)
	log := slog.Default()
	hub := NewHub(log)
	handler := New(store, hub, "test-token", nil, log, nil, nil, nil) // no AI provider
	ts := httptest.NewServer(handler)
	defer ts.Close()

	// POST /api/v1/chat/send should return 501 when AI not configured.
	resp, err := http.Post(ts.URL+"/api/v1/chat/send", "application/json", nil)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusNotImplemented)
	}
}

func TestChatConversationCRUD(t *testing.T) {
	db, err := OpenDB(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	store := NewStore(db)
	log := slog.Default()
	hub := NewHub(log)
	handler := New(store, hub, "test-token", nil, log, nil, nil, nil)
	ts := httptest.NewServer(handler)
	defer ts.Close()

	// Create a conversation directly via store (since send requires AI).
	if err := store.CreateConversation(t.Context(), "test-conv-1", "Test conversation"); err != nil {
		t.Fatalf("create: %v", err)
	}

	// GET /api/v1/chat/conversations
	resp, err := http.Get(ts.URL + "/api/v1/chat/conversations")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	var convos []apitype.Conversation
	if err := json.NewDecoder(resp.Body).Decode(&convos); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(convos) != 1 {
		t.Fatalf("expected 1 conversation, got %d", len(convos))
	}
	if convos[0].Title != "Test conversation" {
		t.Errorf("title = %q, want %q", convos[0].Title, "Test conversation")
	}

	// Insert a message.
	if err := store.InsertChatMessage(t.Context(), "test-conv-1", "user", "hello"); err != nil {
		t.Fatalf("insert: %v", err)
	}

	// GET /api/v1/chat/messages
	resp, err = http.Get(ts.URL + "/api/v1/chat/messages?conversation_id=test-conv-1")
	if err != nil {
		t.Fatalf("get messages: %v", err)
	}
	defer resp.Body.Close()

	var msgs []apitype.ChatMessage
	if err := json.NewDecoder(resp.Body).Decode(&msgs); err != nil {
		t.Fatalf("decode messages: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0].Content != "hello" {
		t.Errorf("content = %q, want %q", msgs[0].Content, "hello")
	}

	// DELETE /api/v1/chat/conversations/test-conv-1
	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/api/v1/chat/conversations/test-conv-1", nil)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("delete status = %d, want %d", resp.StatusCode, http.StatusNoContent)
	}

	// Verify it's gone.
	resp, err = http.Get(ts.URL + "/api/v1/chat/conversations")
	if err != nil {
		t.Fatalf("get after delete: %v", err)
	}
	defer resp.Body.Close()

	if err := json.NewDecoder(resp.Body).Decode(&convos); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(convos) != 0 {
		t.Errorf("expected 0 conversations after delete, got %d", len(convos))
	}
}
