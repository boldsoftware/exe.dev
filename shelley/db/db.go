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

// DB wraps the database connection and provides high-level operations
type DB struct {
	sqlDB   *sql.DB
	Queries *generated.Queries // Exposed directly for users to call sqlc-generated methods
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

	// Ensure directory exists for file-based SQLite databases
	if cfg.DSN != ":memory:" {
		dir := filepath.Dir(cfg.DSN)
		if dir != "." && dir != "" {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return nil, fmt.Errorf("failed to create database directory: %w", err)
			}
		}
	}

	sqlDB, err := sql.Open("sqlite", cfg.DSN+"?_foreign_keys=on&_journal_mode=WAL")
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Test the connection
	if err := sqlDB.Ping(); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	// Ensure foreign keys are enabled
	_, err = sqlDB.Exec("PRAGMA foreign_keys = ON;")
	if err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("failed to enable foreign keys: %w", err)
	}

	return &DB{
		sqlDB:   sqlDB,
		Queries: generated.New(sqlDB),
	}, nil
}

// Close closes the database connection
func (db *DB) Close() error {
	return db.sqlDB.Close()
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

		_, err = db.sqlDB.ExecContext(ctx, string(schemaBytes))
		if err != nil {
			return fmt.Errorf("failed to execute schema %s: %w", file.Name(), err)
		}
	}

	return nil
}

// GetSQLDB returns the underlying sql.DB for transactions or advanced operations
func (db *DB) GetSQLDB() *sql.DB {
	return db.sqlDB
}

// WithTx runs a function within a database transaction
func (db *DB) WithTx(ctx context.Context, fn func(*generated.Queries) error) error {
	tx, err := db.sqlDB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	queries := generated.New(tx)
	if err := fn(queries); err != nil {
		return err
	}

	return tx.Commit()
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
	conversation, err := db.Queries.CreateConversation(ctx, generated.CreateConversationParams{
		ConversationID: conversationID,
		Slug:           slug,
		UserInitiated:  userInitiated,
	})
	return &conversation, err
}

// GetConversationByID retrieves a conversation by its ID
func (db *DB) GetConversationByID(ctx context.Context, conversationID string) (*generated.Conversation, error) {
	conversation, err := db.Queries.GetConversation(ctx, conversationID)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("conversation not found: %s", conversationID)
	}
	return &conversation, err
}

// GetConversationBySlug retrieves a conversation by its slug
func (db *DB) GetConversationBySlug(ctx context.Context, slug string) (*generated.Conversation, error) {
	conversation, err := db.Queries.GetConversationBySlug(ctx, &slug)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("conversation not found with slug: %s", slug)
	}
	return &conversation, err
}

// ListConversations retrieves conversations with pagination
func (db *DB) ListConversations(ctx context.Context, limit, offset int64) ([]generated.Conversation, error) {
	return db.Queries.ListConversations(ctx, generated.ListConversationsParams{
		Limit:  limit,
		Offset: offset,
	})
}

// SearchConversations searches for conversations containing the given query in their slug
func (db *DB) SearchConversations(ctx context.Context, query string, limit, offset int64) ([]generated.Conversation, error) {
	queryPtr := &query
	return db.Queries.SearchConversations(ctx, generated.SearchConversationsParams{
		Column1: queryPtr,
		Limit:   limit,
		Offset:  offset,
	})
}

// UpdateConversationSlug updates the slug of a conversation
func (db *DB) UpdateConversationSlug(ctx context.Context, conversationID, slug string) (*generated.Conversation, error) {
	conversation, err := db.Queries.UpdateConversationSlug(ctx, generated.UpdateConversationSlugParams{
		Slug:           &slug,
		ConversationID: conversationID,
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

	// Get next sequence_id for this conversation
	sequenceID, err := db.Queries.GetNextSequenceID(ctx, params.ConversationID)
	if err != nil {
		return nil, fmt.Errorf("failed to get next sequence ID: %w", err)
	}

	message, err := db.Queries.CreateMessage(ctx, generated.CreateMessageParams{
		MessageID:      messageID,
		ConversationID: params.ConversationID,
		SequenceID:     sequenceID,
		Type:           string(params.Type),
		LlmData:        llmDataJSON,
		UserData:       userDataJSON,
		UsageData:      usageDataJSON,
	})
	return &message, err
}

// GetMessageByID retrieves a message by its ID
func (db *DB) GetMessageByID(ctx context.Context, messageID string) (*generated.Message, error) {
	message, err := db.Queries.GetMessage(ctx, messageID)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("message not found: %s", messageID)
	}
	return &message, err
}

// ListMessagesByConversationPaginated retrieves messages in a conversation with pagination
func (db *DB) ListMessagesByConversationPaginated(ctx context.Context, conversationID string, limit, offset int64) ([]generated.Message, error) {
	return db.Queries.ListMessagesPaginated(ctx, generated.ListMessagesPaginatedParams{
		ConversationID: conversationID,
		Limit:          limit,
		Offset:         offset,
	})
}

// ListMessagesByType retrieves messages of a specific type in a conversation
func (db *DB) ListMessagesByType(ctx context.Context, conversationID string, messageType MessageType) ([]generated.Message, error) {
	return db.Queries.ListMessagesByType(ctx, generated.ListMessagesByTypeParams{
		ConversationID: conversationID,
		Type:           string(messageType),
	})
}

// GetLatestMessage retrieves the latest message in a conversation
func (db *DB) GetLatestMessage(ctx context.Context, conversationID string) (*generated.Message, error) {
	message, err := db.Queries.GetLatestMessage(ctx, conversationID)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("no messages found in conversation: %s", conversationID)
	}
	return &message, err
}

// CountMessagesByType returns the number of messages of a specific type in a conversation
func (db *DB) CountMessagesByType(ctx context.Context, conversationID string, messageType MessageType) (int64, error) {
	return db.Queries.CountMessagesByType(ctx, generated.CountMessagesByTypeParams{
		ConversationID: conversationID,
		Type:           string(messageType),
	})
}
