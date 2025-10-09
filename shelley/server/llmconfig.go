package server

import "log/slog"

// LLMConfig holds all configuration for LLM services
type LLMConfig struct {
	// API keys for each provider
	AnthropicAPIKey string
	OpenAIAPIKey    string
	GeminiAPIKey    string
	FireworksAPIKey string

	// Gateway is the base URL of the LLM gateway (optional)
	Gateway string

	// TerminalURL is the URL to the terminal interface (optional)
	TerminalURL string

	// DefaultModel is the default model to use (optional, defaults to claude-sonnet-4.5)
	DefaultModel string

	Logger *slog.Logger
}
