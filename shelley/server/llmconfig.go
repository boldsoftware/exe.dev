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

	Logger *slog.Logger
}
