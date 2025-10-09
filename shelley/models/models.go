package models

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/ant"
	"shelley.exe.dev/llm/oai"
	"shelley.exe.dev/loop"
)

// Provider represents an LLM provider
type Provider string

const (
	ProviderOpenAI    Provider = "OpenAI"
	ProviderAnthropic Provider = "Anthropic"
	ProviderFireworks Provider = "Fireworks"
	ProviderGemini    Provider = "Gemini"
	ProviderBuiltIn   Provider = "Built-in"
)

// Model represents a configured LLM model in Shelley
type Model struct {
	// ID is the user-facing identifier for this model
	ID string

	// Provider is the LLM provider (OpenAI, Anthropic, etc.)
	Provider Provider

	// Description is a human-readable description
	Description string

	// RequiredEnvVars are the environment variables required for this model
	RequiredEnvVars []string

	// Factory creates an llm.Service instance for this model
	Factory func(config *Config) (llm.Service, error)
}

// Config holds the configuration needed to create LLM services
type Config struct {
	// API keys for each provider
	AnthropicAPIKey string
	OpenAIAPIKey    string
	GeminiAPIKey    string
	FireworksAPIKey string

	// Gateway is the base URL of the LLM gateway (optional)
	// If set, model-specific suffixes will be appended
	Gateway string

	Logger *slog.Logger
}

// getAnthropicURL returns the Anthropic API URL, with gateway suffix if gateway is set
func (c *Config) getAnthropicURL() string {
	if c.Gateway != "" {
		return c.Gateway + "/_/gateway/anthropic/v1/messages"
	}
	return "" // use default from ant package
}

// getOpenAIURL returns the OpenAI API URL, with gateway suffix if gateway is set
func (c *Config) getOpenAIURL() string {
	if c.Gateway != "" {
		return c.Gateway + "/_/gateway/openai/v1"
	}
	return "" // use default from oai package
}

// getGeminiURL returns the Gemini API URL, with gateway suffix if gateway is set
func (c *Config) getGeminiURL() string {
	if c.Gateway != "" {
		return c.Gateway + "/_/gateway/gemini/v1/models/generate"
	}
	return "" // use default from gem package
}

// getFireworksURL returns the Fireworks API URL, with gateway suffix if gateway is set
func (c *Config) getFireworksURL() string {
	if c.Gateway != "" {
		return c.Gateway + "/_/gateway/fireworks/inference/v1"
	}
	return "" // use default from oai package
}

// All returns all available models in Shelley
func All() []Model {
	return []Model{
		{
			ID:              "qwen3-coder-fireworks",
			Provider:        ProviderFireworks,
			Description:     "Qwen3 Coder 480B on Fireworks (default)",
			RequiredEnvVars: []string{"FIREWORKS_API_KEY"},
			Factory: func(config *Config) (llm.Service, error) {
				if config.FireworksAPIKey == "" {
					return nil, fmt.Errorf("qwen3-coder-fireworks requires FIREWORKS_API_KEY")
				}
				svc := &oai.Service{Model: oai.Qwen3CoderFireworks, APIKey: config.FireworksAPIKey}
				if url := config.getFireworksURL(); url != "" {
					svc.ModelURL = url
				}
				return svc, nil
			},
		},
		{
			ID:              "glm-4p6-fireworks",
			Provider:        ProviderFireworks,
			Description:     "GLM-4P6 on Fireworks",
			RequiredEnvVars: []string{"FIREWORKS_API_KEY"},
			Factory: func(config *Config) (llm.Service, error) {
				if config.FireworksAPIKey == "" {
					return nil, fmt.Errorf("glm-4p6-fireworks requires FIREWORKS_API_KEY")
				}
				svc := &oai.Service{Model: oai.GLM4P6Fireworks, APIKey: config.FireworksAPIKey}
				if url := config.getFireworksURL(); url != "" {
					svc.ModelURL = url
				}
				return svc, nil
			},
		},
		{
			ID:              "gpt-5",
			Provider:        ProviderOpenAI,
			Description:     "GPT-5",
			RequiredEnvVars: []string{"OPENAI_API_KEY"},
			Factory: func(config *Config) (llm.Service, error) {
				if config.OpenAIAPIKey == "" {
					return nil, fmt.Errorf("gpt-5 requires OPENAI_API_KEY")
				}
				svc := &oai.Service{Model: oai.GPT5, APIKey: config.OpenAIAPIKey}
				if url := config.getOpenAIURL(); url != "" {
					svc.ModelURL = url
				}
				return svc, nil
			},
		},
		{
			ID:              "gpt-5-nano",
			Provider:        ProviderOpenAI,
			Description:     "GPT-5 Nano",
			RequiredEnvVars: []string{"OPENAI_API_KEY"},
			Factory: func(config *Config) (llm.Service, error) {
				if config.OpenAIAPIKey == "" {
					return nil, fmt.Errorf("gpt-5-nano requires OPENAI_API_KEY")
				}
				svc := &oai.Service{Model: oai.GPT5Nano, APIKey: config.OpenAIAPIKey}
				if url := config.getOpenAIURL(); url != "" {
					svc.ModelURL = url
				}
				return svc, nil
			},
		},
		{
			ID:              "gpt-5-codex",
			Provider:        ProviderOpenAI,
			Description:     "GPT-5 Codex (uses Responses API)",
			RequiredEnvVars: []string{"OPENAI_API_KEY"},
			Factory: func(config *Config) (llm.Service, error) {
				if config.OpenAIAPIKey == "" {
					return nil, fmt.Errorf("gpt-5-codex requires OPENAI_API_KEY")
				}
				svc := &oai.ResponsesService{Model: oai.GPT5Codex, APIKey: config.OpenAIAPIKey}
				if url := config.getOpenAIURL(); url != "" {
					svc.ModelURL = url
				}
				return svc, nil
			},
		},
		{
			ID:              "claude-sonnet-4.5",
			Provider:        ProviderAnthropic,
			Description:     "Claude Sonnet 4.5",
			RequiredEnvVars: []string{"ANTHROPIC_API_KEY"},
			Factory: func(config *Config) (llm.Service, error) {
				if config.AnthropicAPIKey == "" {
					return nil, fmt.Errorf("claude-sonnet-4.5 requires ANTHROPIC_API_KEY")
				}
				svc := &ant.Service{APIKey: config.AnthropicAPIKey, Model: ant.Claude45Sonnet}
				if url := config.getAnthropicURL(); url != "" {
					svc.URL = url
				}
				return svc, nil
			},
		},
		{
			ID:              "claude-haiku-3.5",
			Provider:        ProviderAnthropic,
			Description:     "Claude Haiku 3.5",
			RequiredEnvVars: []string{"ANTHROPIC_API_KEY"},
			Factory: func(config *Config) (llm.Service, error) {
				if config.AnthropicAPIKey == "" {
					return nil, fmt.Errorf("claude-haiku-3.5 requires ANTHROPIC_API_KEY")
				}
				svc := &ant.Service{APIKey: config.AnthropicAPIKey, Model: ant.Claude35Haiku}
				if url := config.getAnthropicURL(); url != "" {
					svc.URL = url
				}
				return svc, nil
			},
		},
		{
			ID:              "predictable",
			Provider:        ProviderBuiltIn,
			Description:     "Deterministic test model (no API key)",
			RequiredEnvVars: []string{},
			Factory: func(config *Config) (llm.Service, error) {
				return loop.NewPredictableService(), nil
			},
		},
	}
}

// ByID returns the model with the given ID, or nil if not found
func ByID(id string) *Model {
	for _, m := range All() {
		if m.ID == id {
			return &m
		}
	}
	return nil
}

// IDs returns all model IDs (not including aliases)
func IDs() []string {
	models := All()
	ids := make([]string, len(models))
	for i, m := range models {
		ids[i] = m.ID
	}
	return ids
}

// Default returns the default model
func Default() Model {
	return All()[0] // qwen3-coder-fireworks
}

// Manager manages LLM services for all configured models
type Manager struct {
	services map[string]llm.Service
	logger   *slog.Logger
}

// NewManager creates a new Manager with all models configured
func NewManager(cfg *Config) (*Manager, error) {
	manager := &Manager{
		services: make(map[string]llm.Service),
		logger:   cfg.Logger,
	}

	for _, model := range All() {
		svc, err := model.Factory(cfg)
		if err != nil {
			// Model not available (e.g., missing API key) - skip it
			continue
		}
		manager.services[model.ID] = svc
	}

	return manager, nil
}

// GetService returns the LLM service for the given model ID, wrapped with logging
func (m *Manager) GetService(modelID string) (llm.Service, error) {
	if svc, ok := m.services[modelID]; ok {
		// Wrap with logging if we have a logger
		if m.logger != nil {
			return &loggingService{
				service: svc,
				logger:  m.logger,
				modelID: modelID,
			}, nil
		}
		return svc, nil
	}
	return nil, fmt.Errorf("unsupported model: %s", modelID)
}

// GetAvailableModels returns a list of available model IDs in the same order as All()
func (m *Manager) GetAvailableModels() []string {
	// Return IDs in the same order as All() for consistency
	all := All()
	var ids []string
	for _, model := range all {
		if _, ok := m.services[model.ID]; ok {
			ids = append(ids, model.ID)
		}
	}
	return ids
}

// HasModel reports whether the manager has a service for the given model ID
func (m *Manager) HasModel(modelID string) bool {
	_, ok := m.services[modelID]
	return ok
}

// loggingService wraps an llm.Service to log request completion
type loggingService struct {
	service llm.Service
	logger  *slog.Logger
	modelID string
}

// ConfigInfo is an optional interface that services can implement to provide configuration details
type ConfigInfo interface {
	ConfigDetails() map[string]string
}

// Do wraps the underlying service's Do method with logging
func (l *loggingService) Do(ctx context.Context, request *llm.Request) (*llm.Response, error) {
	start := time.Now()
	response, err := l.service.Do(ctx, request)
	duration := time.Since(start)

	if err != nil {
		logAttrs := []any{
			"model", l.modelID,
			"duration_seconds", duration.Seconds(),
		}
		if configProvider, ok := l.service.(ConfigInfo); ok {
			for k, v := range configProvider.ConfigDetails() {
				logAttrs = append(logAttrs, k, v)
			}
		}
		logAttrs = append(logAttrs, "error", err)
		l.logger.Error("LLM request failed", logAttrs...)
	} else {
		logAttrs := []any{
			"model", l.modelID,
			"duration_seconds", duration.Seconds(),
		}
		if !response.Usage.IsZero() {
			logAttrs = append(logAttrs,
				"input_tokens", response.Usage.InputTokens,
				"output_tokens", response.Usage.OutputTokens,
				"cost_usd", response.Usage.CostUSD,
			)
			if response.Usage.CacheCreationInputTokens > 0 {
				logAttrs = append(logAttrs, "cache_creation_input_tokens", response.Usage.CacheCreationInputTokens)
			}
			if response.Usage.CacheReadInputTokens > 0 {
				logAttrs = append(logAttrs, "cache_read_input_tokens", response.Usage.CacheReadInputTokens)
			}
		}
		l.logger.Info("LLM request completed", logAttrs...)
	}

	return response, err
}

// TokenContextWindow delegates to the underlying service
func (l *loggingService) TokenContextWindow() int {
	return l.service.TokenContextWindow()
}

// UseSimplifiedPatch delegates to the underlying service if it supports it
func (l *loggingService) UseSimplifiedPatch() bool {
	if sp, ok := l.service.(llm.SimplifiedPatcher); ok {
		return sp.UseSimplifiedPatch()
	}
	return false
}
