package server

import (
	"context"
	"testing"
	"time"
)

func TestCreateAndListConversations(t *testing.T) {
	store := testDB(t)
	ctx := context.Background()

	if err := store.CreateConversation(ctx, "conv-1", "Hello world"); err != nil {
		t.Fatalf("create conversation: %v", err)
	}

	convos, err := store.ListConversations(ctx)
	if err != nil {
		t.Fatalf("list conversations: %v", err)
	}
	if len(convos) != 1 {
		t.Fatalf("expected 1 conversation, got %d", len(convos))
	}
	if convos[0].ID != "conv-1" {
		t.Errorf("id = %q, want %q", convos[0].ID, "conv-1")
	}
	if convos[0].Title != "Hello world" {
		t.Errorf("title = %q, want %q", convos[0].Title, "Hello world")
	}
}

func TestDeleteConversation(t *testing.T) {
	store := testDB(t)
	ctx := context.Background()

	if err := store.CreateConversation(ctx, "conv-del", "To be deleted"); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := store.InsertChatMessage(ctx, "conv-del", "user", "hello"); err != nil {
		t.Fatalf("insert message: %v", err)
	}

	if err := store.DeleteConversation(ctx, "conv-del"); err != nil {
		t.Fatalf("delete: %v", err)
	}

	convos, err := store.ListConversations(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(convos) != 0 {
		t.Errorf("expected 0 conversations after delete, got %d", len(convos))
	}

	// Messages should be gone too (CASCADE).
	msgs, err := store.ListChatMessages(ctx, "conv-del")
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(msgs) != 0 {
		t.Errorf("expected 0 messages after cascade delete, got %d", len(msgs))
	}
}

func TestDeleteConversationNotFound(t *testing.T) {
	store := testDB(t)
	ctx := context.Background()

	err := store.DeleteConversation(ctx, "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent conversation")
	}
}

func TestInsertAndListChatMessages(t *testing.T) {
	store := testDB(t)
	ctx := context.Background()

	if err := store.CreateConversation(ctx, "conv-msg", "Messages test"); err != nil {
		t.Fatalf("create: %v", err)
	}

	tests := []struct {
		role    string
		content string
	}{
		{"user", "What's the fleet status?"},
		{"assistant", "All servers are healthy."},
		{"user", "How about disk usage?"},
		{"assistant", "Disk usage is within normal parameters."},
	}

	for _, tt := range tests {
		if err := store.InsertChatMessage(ctx, "conv-msg", tt.role, tt.content); err != nil {
			t.Fatalf("insert %s message: %v", tt.role, err)
		}
	}

	msgs, err := store.ListChatMessages(ctx, "conv-msg")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(msgs) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(msgs))
	}

	// Verify ordering (ASC by id).
	for i, tt := range tests {
		if msgs[i].Role != tt.role {
			t.Errorf("msg[%d].role = %q, want %q", i, msgs[i].Role, tt.role)
		}
		if msgs[i].Content != tt.content {
			t.Errorf("msg[%d].content = %q, want %q", i, msgs[i].Content, tt.content)
		}
		if msgs[i].ConversationID != "conv-msg" {
			t.Errorf("msg[%d].conversation_id = %q, want %q", i, msgs[i].ConversationID, "conv-msg")
		}
	}
}

func TestListChatMessagesEmpty(t *testing.T) {
	store := testDB(t)
	ctx := context.Background()

	msgs, err := store.ListChatMessages(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(msgs) != 0 {
		t.Errorf("expected 0 messages, got %d", len(msgs))
	}
}

func TestPurgeOldConversations(t *testing.T) {
	store := testDB(t)
	ctx := context.Background()

	if err := store.CreateConversation(ctx, "conv-old", "Old conversation"); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Backdate the conversation.
	cutoff := time.Now().UTC().Add(-31 * 24 * time.Hour).Format(time.RFC3339)
	_, err := store.db.ExecContext(ctx, "UPDATE chat_conversations SET updated_at = ? WHERE id = ?", cutoff, "conv-old")
	if err != nil {
		t.Fatalf("backdate: %v", err)
	}

	if err := store.CreateConversation(ctx, "conv-new", "New conversation"); err != nil {
		t.Fatalf("create new: %v", err)
	}

	deleted, err := store.PurgeOldConversations(ctx, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if deleted != 1 {
		t.Errorf("deleted = %d, want 1", deleted)
	}

	convos, err := store.ListConversations(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(convos) != 1 {
		t.Fatalf("expected 1 conversation remaining, got %d", len(convos))
	}
	if convos[0].ID != "conv-new" {
		t.Errorf("remaining conversation = %q, want %q", convos[0].ID, "conv-new")
	}
}

func TestConversationUpdatedAtOnMessage(t *testing.T) {
	store := testDB(t)
	ctx := context.Background()

	if err := store.CreateConversation(ctx, "conv-ts", "Timestamp test"); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Backdate the conversation so there's a guaranteed difference.
	old := time.Now().UTC().Add(-10 * time.Second).Format(time.RFC3339)
	_, err := store.db.ExecContext(ctx, "UPDATE chat_conversations SET updated_at = ? WHERE id = ?", old, "conv-ts")
	if err != nil {
		t.Fatalf("backdate: %v", err)
	}

	convos, err := store.ListConversations(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	originalUpdated := convos[0].UpdatedAt

	// Insert a message — updated_at should change.
	if err := store.InsertChatMessage(ctx, "conv-ts", "user", "test"); err != nil {
		t.Fatalf("insert: %v", err)
	}

	convos, err = store.ListConversations(ctx)
	if err != nil {
		t.Fatalf("list after insert: %v", err)
	}
	if convos[0].UpdatedAt == originalUpdated {
		t.Error("expected updated_at to change after inserting a message")
	}
}
