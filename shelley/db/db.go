// Package db provides database operations for the Shelley AI coding agent.
package db

import (
	"context"
	"crypto/rand"
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/google/uuid"
	"shelley.exe.dev/db/generated"

	_ "modernc.org/sqlite"
)

//go:embed schema/*.sql
var schemaFS embed.FS

// generateConversationID generates a conversation ID in the format "cXXXXXX"
// where X are random alphanumeric characters
func generateConversationID() (string, error) {
	text := rand.Text()
	if len(text) < 6 {
		return "", fmt.Errorf("rand.Text() returned insufficient characters: %d", len(text))
	}
	return "c" + text[:6], nil
}

// DB wraps the database connection pool and provides high-level operations
type DB struct {
	pool *Pool
}

// Config holds database configuration
type Config struct {
	DSN string // Data Source Name for SQLite database
}

// New creates a new database connection with the given configuration
func New(cfg Config) (*DB, error) {
	if cfg.DSN == "" {
		return nil, fmt.Errorf("database DSN cannot be empty")
	}

	if cfg.DSN == ":memory:" {
		return nil, fmt.Errorf(":memory: database not supported (requires multiple connections); use a temp file")
	}

	// Ensure directory exists for file-based SQLite databases
	if cfg.DSN != ":memory:" {
		dir := filepath.Dir(cfg.DSN)
		if dir != "." && dir != "" {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return nil, fmt.Errorf("failed to create database directory: %w", err)
			}
		}
	}

	// Create connection pool with 3 readers
	dsn := cfg.DSN
	if !strings.Contains(dsn, "?") {
		dsn += "?_foreign_keys=on"
	} else if !strings.Contains(dsn, "_foreign_keys") {
		dsn += "&_foreign_keys=on"
	}

	pool, err := NewPool(dsn, 3)
	if err != nil {
		return nil, fmt.Errorf("failed to create connection pool: %w", err)
	}

	return &DB{
		pool: pool,
	}, nil
}

// Close closes the database connection pool
func (db *DB) Close() error {
	return db.pool.Close()
}

// Migrate runs the database migrations
func (db *DB) Migrate(ctx context.Context) error {
	// Read all schema files from the embedded filesystem
	schemaFiles, err := schemaFS.ReadDir("schema")
	if err != nil {
		return fmt.Errorf("failed to read schema directory: %w", err)
	}

	// Sort files by name to ensure lexicographic order
	sort.Slice(schemaFiles, func(i, j int) bool {
		return schemaFiles[i].Name() < schemaFiles[j].Name()
	})

	// Execute each schema file in order
	for _, file := range schemaFiles {
		if file.IsDir() {
			continue
		}

		schemaBytes, err := schemaFS.ReadFile(fmt.Sprintf("schema/%s", file.Name()))
		if err != nil {
			return fmt.Errorf("failed to read schema file %s: %w", file.Name(), err)
		}

		if err := db.pool.Exec(ctx, string(schemaBytes)); err != nil {
			return fmt.Errorf("failed to execute schema %s: %w", file.Name(), err)
		}
	}

	return nil
}

// Pool returns the underlying connection pool for advanced operations
func (db *DB) Pool() *Pool {
	return db.pool
}

// WithTx runs a function within a database transaction
func (db *DB) WithTx(ctx context.Context, fn func(*generated.Queries) error) error {
	return db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		queries := generated.New(tx.Conn())
		return fn(queries)
	})
}

// WithTxRes runs a function within a database transaction and returns a value
func WithTxRes[T any](db *DB, ctx context.Context, fn func(*generated.Queries) (T, error)) (T, error) {
	var result T
	err := db.WithTx(ctx, func(queries *generated.Queries) error {
		var err error
		result, err = fn(queries)
		return err
	})
	return result, err
}

// Conversation methods (moved from ConversationService)

// CreateConversation creates a new conversation with an optional slug
func (db *DB) CreateConversation(ctx context.Context, slug *string, userInitiated bool) (*generated.Conversation, error) {
	conversationID, err := generateConversationID()
	if err != nil {
		return nil, fmt.Errorf("failed to generate conversation ID: %w", err)
	}
	var conversation generated.Conversation
	err = db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		q := generated.New(tx.Conn())
		conversation, err = q.CreateConversation(ctx, generated.CreateConversationParams{
			ConversationID: conversationID,
			Slug:           slug,
			UserInitiated:  userInitiated,
		})
		return err
	})
	return &conversation, err
}

// GetConversationByID retrieves a conversation by its ID
func (db *DB) GetConversationByID(ctx context.Context, conversationID string) (*generated.Conversation, error) {
	var conversation generated.Conversation
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		q := generated.New(rx.Conn())
		var err error
		conversation, err = q.GetConversation(ctx, conversationID)
		return err
	})
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("conversation not found: %s", conversationID)
	}
	return &conversation, err
}

// GetConversationBySlug retrieves a conversation by its slug
func (db *DB) GetConversationBySlug(ctx context.Context, slug string) (*generated.Conversation, error) {
	var conversation generated.Conversation
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		q := generated.New(rx.Conn())
		var err error
		conversation, err = q.GetConversationBySlug(ctx, &slug)
		return err
	})
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("conversation not found with slug: %s", slug)
	}
	return &conversation, err
}

// ListConversations retrieves conversations with pagination
func (db *DB) ListConversations(ctx context.Context, limit, offset int64) ([]generated.Conversation, error) {
	var conversations []generated.Conversation
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		q := generated.New(rx.Conn())
		var err error
		conversations, err = q.ListConversations(ctx, generated.ListConversationsParams{
			Limit:  limit,
			Offset: offset,
		})
		return err
	})
	return conversations, err
}

// SearchConversations searches for conversations containing the given query in their slug
func (db *DB) SearchConversations(ctx context.Context, query string, limit, offset int64) ([]generated.Conversation, error) {
	queryPtr := &query
	var conversations []generated.Conversation
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		q := generated.New(rx.Conn())
		var err error
		conversations, err = q.SearchConversations(ctx, generated.SearchConversationsParams{
			Column1: queryPtr,
			Limit:   limit,
			Offset:  offset,
		})
		return err
	})
	return conversations, err
}

// UpdateConversationSlug updates the slug of a conversation
func (db *DB) UpdateConversationSlug(ctx context.Context, conversationID, slug string) (*generated.Conversation, error) {
	var conversation generated.Conversation
	err := db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		q := generated.New(tx.Conn())
		var err error
		conversation, err = q.UpdateConversationSlug(ctx, generated.UpdateConversationSlugParams{
			Slug:           &slug,
			ConversationID: conversationID,
		})
		return err
	})
	return &conversation, err
}

// Message methods (moved from MessageService)

// MessageType represents the type of message
type MessageType string

const (
	MessageTypeUser   MessageType = "user"
	MessageTypeAgent  MessageType = "agent"
	MessageTypeTool   MessageType = "tool"
	MessageTypeSystem MessageType = "system"
	MessageTypeError  MessageType = "error"
)

// CreateMessageParams contains parameters for creating a message
type CreateMessageParams struct {
	ConversationID string
	Type           MessageType
	LLMData        interface{} // Will be JSON marshalled
	UserData       interface{} // Will be JSON marshalled
	UsageData      interface{} // Will be JSON marshalled
}

// CreateMessage creates a new message
func (db *DB) CreateMessage(ctx context.Context, params CreateMessageParams) (*generated.Message, error) {
	messageID := uuid.New().String()

	// Marshal JSON fields
	var llmDataJSON, userDataJSON, usageDataJSON *string

	if params.LLMData != nil {
		data, err := json.Marshal(params.LLMData)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal LLM data: %w", err)
		}
		str := string(data)
		llmDataJSON = &str
	}

	if params.UserData != nil {
		data, err := json.Marshal(params.UserData)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal user data: %w", err)
		}
		str := string(data)
		userDataJSON = &str
	}

	if params.UsageData != nil {
		data, err := json.Marshal(params.UsageData)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal usage data: %w", err)
		}
		str := string(data)
		usageDataJSON = &str
	}

	var message generated.Message
	err := db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		q := generated.New(tx.Conn())

		// Get next sequence_id for this conversation
		sequenceID, err := q.GetNextSequenceID(ctx, params.ConversationID)
		if err != nil {
			return fmt.Errorf("failed to get next sequence ID: %w", err)
		}

		message, err = q.CreateMessage(ctx, generated.CreateMessageParams{
			MessageID:      messageID,
			ConversationID: params.ConversationID,
			SequenceID:     sequenceID,
			Type:           string(params.Type),
			LlmData:        llmDataJSON,
			UserData:       userDataJSON,
			UsageData:      usageDataJSON,
		})
		return err
	})
	return &message, err
}

// GetMessageByID retrieves a message by its ID
func (db *DB) GetMessageByID(ctx context.Context, messageID string) (*generated.Message, error) {
	var message generated.Message
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		q := generated.New(rx.Conn())
		var err error
		message, err = q.GetMessage(ctx, messageID)
		return err
	})
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("message not found: %s", messageID)
	}
	return &message, err
}

// ListMessagesByConversationPaginated retrieves messages in a conversation with pagination
func (db *DB) ListMessagesByConversationPaginated(ctx context.Context, conversationID string, limit, offset int64) ([]generated.Message, error) {
	var messages []generated.Message
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		q := generated.New(rx.Conn())
		var err error
		messages, err = q.ListMessagesPaginated(ctx, generated.ListMessagesPaginatedParams{
			ConversationID: conversationID,
			Limit:          limit,
			Offset:         offset,
		})
		return err
	})
	return messages, err
}

// ListMessagesByType retrieves messages of a specific type in a conversation
func (db *DB) ListMessagesByType(ctx context.Context, conversationID string, messageType MessageType) ([]generated.Message, error) {
	var messages []generated.Message
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		q := generated.New(rx.Conn())
		var err error
		messages, err = q.ListMessagesByType(ctx, generated.ListMessagesByTypeParams{
			ConversationID: conversationID,
			Type:           string(messageType),
		})
		return err
	})
	return messages, err
}

// GetLatestMessage retrieves the latest message in a conversation
func (db *DB) GetLatestMessage(ctx context.Context, conversationID string) (*generated.Message, error) {
	var message generated.Message
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		q := generated.New(rx.Conn())
		var err error
		message, err = q.GetLatestMessage(ctx, conversationID)
		return err
	})
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("no messages found in conversation: %s", conversationID)
	}
	return &message, err
}

// CountMessagesByType returns the number of messages of a specific type in a conversation
func (db *DB) CountMessagesByType(ctx context.Context, conversationID string, messageType MessageType) (int64, error) {
	var count int64
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		q := generated.New(rx.Conn())
		var err error
		count, err = q.CountMessagesByType(ctx, generated.CountMessagesByTypeParams{
			ConversationID: conversationID,
			Type:           string(messageType),
		})
		return err
	})
	return count, err
}

// Queries provides read-only access to generated queries within a read transaction
func (db *DB) Queries(ctx context.Context, fn func(*generated.Queries) error) error {
	return db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		q := generated.New(rx.Conn())
		return fn(q)
	})
}

// QueriesTx provides read-write access to generated queries within a write transaction
func (db *DB) QueriesTx(ctx context.Context, fn func(*generated.Queries) error) error {
	return db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		q := generated.New(tx.Conn())
		return fn(q)
	})
}
