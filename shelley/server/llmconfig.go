package server

import "log/slog"

// LLMConfig holds all configuration for LLM services
type LLMConfig struct {
	// API keys for each provider
	AnthropicAPIKey string
	OpenAIAPIKey    string
	GeminiAPIKey    string
	FireworksAPIKey string

	// Base URLs for each provider (optional, uses defaults if empty)
	AnthropicBaseURL string
	OpenAIBaseURL    string
	GeminiBaseURL    string
	FireworksBaseURL string

	Logger *slog.Logger
}
