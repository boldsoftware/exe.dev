package aiagent

import (
	"context"
	"encoding/json"
	"fmt"
)

// Message represents a chat message in a conversation.
type Message struct {
	Role       string     `json:"role"`                   // "system", "user", "assistant", "tool"
	Content    string     `json:"content"`                // text content
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`   // tool calls from assistant
	ToolCallID string     `json:"tool_call_id,omitempty"` // for role="tool" responses
}

// ToolCall represents an AI-requested tool invocation.
type ToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// Tool defines a tool the AI can call.
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

// StreamEvent represents a single event from the streaming response.
type StreamEvent struct {
	Type     string    // "text", "tool_call", "done", "error"
	Text     string    // for "text" events
	ToolCall *ToolCall // for "tool_call" events
	Error    string    // for "error" events
}

// Provider defines the interface for AI chat providers.
type Provider interface {
	ChatStream(ctx context.Context, messages []Message, tools []Tool) (<-chan StreamEvent, error)
}

// Config holds the configuration for creating an AI provider.
type Config struct {
	Provider string // "anthropic", "openai", "openai-compat", "ollama"
	APIKey   string
	Model    string
	BaseURL  string
}

// NewProvider creates a Provider from the given configuration.
// It applies default base URLs and models for known providers,
// populating cfg fields in place so the caller can log the effective values.
func NewProvider(cfg *Config) (Provider, error) {
	switch cfg.Provider {
	case "anthropic":
		if cfg.APIKey == "" {
			return nil, fmt.Errorf("anthropic provider requires an API key")
		}
		if cfg.BaseURL == "" {
			cfg.BaseURL = "https://api.anthropic.com"
		}
		if cfg.Model == "" {
			cfg.Model = "claude-sonnet-4-20250514"
		}
		return &anthropicProvider{apiKey: cfg.APIKey, model: cfg.Model, baseURL: cfg.BaseURL}, nil
	case "openai":
		if cfg.APIKey == "" {
			return nil, fmt.Errorf("openai provider requires an API key")
		}
		if cfg.BaseURL == "" {
			cfg.BaseURL = "https://api.openai.com/v1"
		}
		if cfg.Model == "" {
			cfg.Model = "gpt-4o"
		}
		return &openaiProvider{apiKey: cfg.APIKey, model: cfg.Model, baseURL: cfg.BaseURL}, nil
	case "openai-compat":
		if cfg.BaseURL == "" {
			return nil, fmt.Errorf("openai-compat provider requires a base URL")
		}
		if cfg.Model == "" {
			cfg.Model = "default"
		}
		return &openaiProvider{apiKey: cfg.APIKey, model: cfg.Model, baseURL: cfg.BaseURL}, nil
	case "ollama":
		if cfg.BaseURL == "" {
			cfg.BaseURL = "http://localhost:11434"
		}
		if cfg.Model == "" {
			cfg.Model = "llama3"
		}
		return &ollamaProvider{model: cfg.Model, baseURL: cfg.BaseURL}, nil
	default:
		return nil, fmt.Errorf("unknown AI provider: %q", cfg.Provider)
	}
}
