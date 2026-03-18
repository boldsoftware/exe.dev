package server

import (
	"context"
	"fmt"
	"time"

	"exe.dev/exe-ops/apitype"
)

// CreateConversation inserts a new conversation.
func (s *Store) CreateConversation(ctx context.Context, id, title string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.ExecContext(ctx,
		"INSERT INTO chat_conversations (id, title, created_at, updated_at) VALUES (?, ?, ?, ?)",
		id, title, now, now,
	)
	if err != nil {
		return fmt.Errorf("create conversation: %w", err)
	}
	return nil
}

// ListConversations returns all conversations ordered by most recent.
func (s *Store) ListConversations(ctx context.Context) ([]apitype.Conversation, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT id, title, created_at, updated_at FROM chat_conversations ORDER BY updated_at DESC",
	)
	if err != nil {
		return nil, fmt.Errorf("list conversations: %w", err)
	}
	defer rows.Close()

	var convos []apitype.Conversation
	for rows.Next() {
		var c apitype.Conversation
		if err := rows.Scan(&c.ID, &c.Title, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan conversation: %w", err)
		}
		convos = append(convos, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate conversations: %w", err)
	}
	if convos == nil {
		convos = []apitype.Conversation{}
	}
	return convos, nil
}

// UpdateConversationTitle updates the title of a conversation.
func (s *Store) UpdateConversationTitle(ctx context.Context, id, title string) error {
	res, err := s.db.ExecContext(ctx, "UPDATE chat_conversations SET title = ? WHERE id = ?", title, id)
	if err != nil {
		return fmt.Errorf("update conversation title: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("conversation %q not found", id)
	}
	return nil
}

// DeleteConversation deletes a conversation and its messages (via CASCADE).
func (s *Store) DeleteConversation(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, "DELETE FROM chat_conversations WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete conversation: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("conversation %q not found", id)
	}
	return nil
}

// InsertChatMessage inserts a message into a conversation and updates the conversation timestamp.
func (s *Store) InsertChatMessage(ctx context.Context, conversationID, role, content string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.ExecContext(ctx,
		"INSERT INTO chat_messages (conversation_id, role, content, created_at) VALUES (?, ?, ?, ?)",
		conversationID, role, content, now,
	)
	if err != nil {
		return fmt.Errorf("insert chat message: %w", err)
	}
	_, err = s.db.ExecContext(ctx,
		"UPDATE chat_conversations SET updated_at = ? WHERE id = ?",
		now, conversationID,
	)
	if err != nil {
		return fmt.Errorf("update conversation timestamp: %w", err)
	}
	return nil
}

// ListChatMessages returns all messages for a conversation in order.
func (s *Store) ListChatMessages(ctx context.Context, conversationID string) ([]apitype.ChatMessage, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT id, conversation_id, role, content, created_at FROM chat_messages WHERE conversation_id = ? ORDER BY id ASC",
		conversationID,
	)
	if err != nil {
		return nil, fmt.Errorf("list chat messages: %w", err)
	}
	defer rows.Close()

	var messages []apitype.ChatMessage
	for rows.Next() {
		var m apitype.ChatMessage
		if err := rows.Scan(&m.ID, &m.ConversationID, &m.Role, &m.Content, &m.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan chat message: %w", err)
		}
		messages = append(messages, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate chat messages: %w", err)
	}
	if messages == nil {
		messages = []apitype.ChatMessage{}
	}
	return messages, nil
}

// PurgeOldConversations deletes conversations older than the given duration.
func (s *Store) PurgeOldConversations(ctx context.Context, maxAge time.Duration) (int64, error) {
	cutoff := time.Now().UTC().Add(-maxAge).Format(time.RFC3339)
	res, err := s.db.ExecContext(ctx, "DELETE FROM chat_conversations WHERE updated_at < ?", cutoff)
	if err != nil {
		return 0, fmt.Errorf("purge old conversations: %w", err)
	}
	return res.RowsAffected()
}
