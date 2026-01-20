package models

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/ant"
	"shelley.exe.dev/llm/gem"
	"shelley.exe.dev/llm/llmhttp"
	"shelley.exe.dev/llm/oai"
	"shelley.exe.dev/loop"
)

// Provider represents an LLM provider
type Provider string

const (
	ProviderOpenAI    Provider = "openai"
	ProviderAnthropic Provider = "anthropic"
	ProviderFireworks Provider = "fireworks"
	ProviderGemini    Provider = "gemini"
	ProviderBuiltIn   Provider = "builtin"
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
	Factory func(config *Config, httpc *http.Client) (llm.Service, error)
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

	// Database for recording LLM requests (optional)
	DB *db.DB
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
			ID:              "claude-opus-4.5",
			Provider:        ProviderAnthropic,
			Description:     "Claude Opus 4.5 (default)",
			RequiredEnvVars: []string{"ANTHROPIC_API_KEY"},
			Factory: func(config *Config, httpc *http.Client) (llm.Service, error) {
				if config.AnthropicAPIKey == "" {
					return nil, fmt.Errorf("claude-opus-4.5 requires ANTHROPIC_API_KEY")
				}
				svc := &ant.Service{APIKey: config.AnthropicAPIKey, Model: ant.Claude45Opus, HTTPC: httpc}
				if url := config.getAnthropicURL(); url != "" {
					svc.URL = url
				}
				return svc, nil
			},
		},
		{
			ID:              "qwen3-coder-fireworks",
			Provider:        ProviderFireworks,
			Description:     "Qwen3 Coder 480B on Fireworks",
			RequiredEnvVars: []string{"FIREWORKS_API_KEY"},
			Factory: func(config *Config, httpc *http.Client) (llm.Service, error) {
				if config.FireworksAPIKey == "" {
					return nil, fmt.Errorf("qwen3-coder-fireworks requires FIREWORKS_API_KEY")
				}
				svc := &oai.Service{Model: oai.Qwen3CoderFireworks, APIKey: config.FireworksAPIKey, HTTPC: httpc}
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
			Factory: func(config *Config, httpc *http.Client) (llm.Service, error) {
				if config.FireworksAPIKey == "" {
					return nil, fmt.Errorf("glm-4p6-fireworks requires FIREWORKS_API_KEY")
				}
				svc := &oai.Service{Model: oai.GLM4P6Fireworks, APIKey: config.FireworksAPIKey, HTTPC: httpc}
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
			Factory: func(config *Config, httpc *http.Client) (llm.Service, error) {
				if config.OpenAIAPIKey == "" {
					return nil, fmt.Errorf("gpt-5 requires OPENAI_API_KEY")
				}
				svc := &oai.Service{Model: oai.GPT5, APIKey: config.OpenAIAPIKey, HTTPC: httpc}
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
			Factory: func(config *Config, httpc *http.Client) (llm.Service, error) {
				if config.OpenAIAPIKey == "" {
					return nil, fmt.Errorf("gpt-5-nano requires OPENAI_API_KEY")
				}
				svc := &oai.Service{Model: oai.GPT5Nano, APIKey: config.OpenAIAPIKey, HTTPC: httpc}
				if url := config.getOpenAIURL(); url != "" {
					svc.ModelURL = url
				}
				return svc, nil
			},
		},
		{
			ID:              "gpt-5.1-codex",
			Provider:        ProviderOpenAI,
			Description:     "GPT-5.1 Codex (uses Responses API)",
			RequiredEnvVars: []string{"OPENAI_API_KEY"},
			Factory: func(config *Config, httpc *http.Client) (llm.Service, error) {
				if config.OpenAIAPIKey == "" {
					return nil, fmt.Errorf("gpt-5.1-codex requires OPENAI_API_KEY")
				}
				svc := &oai.ResponsesService{Model: oai.GPT5Codex, APIKey: config.OpenAIAPIKey, HTTPC: httpc}
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
			Factory: func(config *Config, httpc *http.Client) (llm.Service, error) {
				if config.AnthropicAPIKey == "" {
					return nil, fmt.Errorf("claude-sonnet-4.5 requires ANTHROPIC_API_KEY")
				}
				svc := &ant.Service{APIKey: config.AnthropicAPIKey, Model: ant.Claude45Sonnet, HTTPC: httpc}
				if url := config.getAnthropicURL(); url != "" {
					svc.URL = url
				}
				return svc, nil
			},
		},
		{
			ID:              "claude-haiku-4.5",
			Provider:        ProviderAnthropic,
			Description:     "Claude Haiku 4.5",
			RequiredEnvVars: []string{"ANTHROPIC_API_KEY"},
			Factory: func(config *Config, httpc *http.Client) (llm.Service, error) {
				if config.AnthropicAPIKey == "" {
					return nil, fmt.Errorf("claude-haiku-4.5 requires ANTHROPIC_API_KEY")
				}
				svc := &ant.Service{APIKey: config.AnthropicAPIKey, Model: ant.Claude45Haiku, HTTPC: httpc}
				if url := config.getAnthropicURL(); url != "" {
					svc.URL = url
				}
				return svc, nil
			},
		},
		{
			ID:              "gemini-2.5-pro",
			Provider:        ProviderGemini,
			Description:     "Gemini 2.5 Pro",
			RequiredEnvVars: []string{"GEMINI_API_KEY"},
			Factory: func(config *Config, httpc *http.Client) (llm.Service, error) {
				if config.GeminiAPIKey == "" {
					return nil, fmt.Errorf("gemini-2.5-pro requires GEMINI_API_KEY")
				}
				svc := &gem.Service{APIKey: config.GeminiAPIKey, Model: gem.DefaultModel, HTTPC: httpc}
				if url := config.getGeminiURL(); url != "" {
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
			Factory: func(config *Config, httpc *http.Client) (llm.Service, error) {
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
	return All()[0] // claude-opus-4.5
}

// Manager manages LLM services for all configured models
type Manager struct {
	services map[string]serviceEntry
	logger   *slog.Logger
	db       *db.DB
}

type serviceEntry struct {
	service  llm.Service
	provider Provider
	modelID  string
}

// ConfigInfo is an optional interface that services can implement to provide configuration details for logging
type ConfigInfo interface {
	// ConfigDetails returns human-readable configuration info (e.g., URL, model name)
	ConfigDetails() map[string]string
}

// loggingService wraps an llm.Service to log request completion with usage information
type loggingService struct {
	service  llm.Service
	logger   *slog.Logger
	modelID  string
	provider Provider
	db       *db.DB
}

// Do wraps the underlying service's Do method with logging and database recording
func (l *loggingService) Do(ctx context.Context, request *llm.Request) (*llm.Response, error) {
	start := time.Now()

	// Add model ID and provider to context for the HTTP transport
	ctx = llmhttp.WithModelID(ctx, l.modelID)
	ctx = llmhttp.WithProvider(ctx, string(l.provider))

	// Call the underlying service
	response, err := l.service.Do(ctx, request)

	duration := time.Since(start)
	durationSeconds := duration.Seconds()

	// Log the completion with usage information
	if err != nil {
		logAttrs := []any{
			"model", l.modelID,
			"duration_seconds", durationSeconds,
		}

		// Add configuration details if available
		if configProvider, ok := l.service.(ConfigInfo); ok {
			for k, v := range configProvider.ConfigDetails() {
				logAttrs = append(logAttrs, k, v)
			}
		}

		logAttrs = append(logAttrs, "error", err)
		l.logger.Error("LLM request failed", logAttrs...)
	} else {
		// Log successful completion with usage info
		logAttrs := []any{
			"model", l.modelID,
			"duration_seconds", durationSeconds,
		}

		// Add usage information if available
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

// MaxImageDimension delegates to the underlying service
func (l *loggingService) MaxImageDimension() int {
	return l.service.MaxImageDimension()
}

// UseSimplifiedPatch delegates to the underlying service if it supports it
func (l *loggingService) UseSimplifiedPatch() bool {
	if sp, ok := l.service.(llm.SimplifiedPatcher); ok {
		return sp.UseSimplifiedPatch()
	}
	return false
}

// NewManager creates a new Manager with all models configured
func NewManager(cfg *Config) (*Manager, error) {
	manager := &Manager{
		services: make(map[string]serviceEntry),
		logger:   cfg.Logger,
		db:       cfg.DB,
	}

	// Create HTTP client with recording if database is available
	var httpc *http.Client
	if cfg.DB != nil {
		recorder := func(ctx context.Context, url string, requestBody, responseBody []byte, statusCode int, err error, duration time.Duration) {
			modelID := llmhttp.ModelIDFromContext(ctx)
			provider := llmhttp.ProviderFromContext(ctx)
			conversationID := llmhttp.ConversationIDFromContext(ctx)

			var convIDPtr *string
			if conversationID != "" {
				convIDPtr = &conversationID
			}

			var reqBodyPtr, respBodyPtr *string
			if len(requestBody) > 0 {
				s := string(requestBody)
				reqBodyPtr = &s
			}
			if len(responseBody) > 0 {
				s := string(responseBody)
				respBodyPtr = &s
			}

			var statusCodePtr *int64
			if statusCode != 0 {
				sc := int64(statusCode)
				statusCodePtr = &sc
			}

			var errPtr *string
			if err != nil {
				s := err.Error()
				errPtr = &s
			}

			durationMs := duration.Milliseconds()
			durationMsPtr := &durationMs

			// Insert into database (fire and forget, don't block the request)
			go func() {
				_, insertErr := cfg.DB.InsertLLMRequest(context.Background(), generated.InsertLLMRequestParams{
					ConversationID: convIDPtr,
					Model:          modelID,
					Provider:       provider,
					Url:            url,
					RequestBody:    reqBodyPtr,
					ResponseBody:   respBodyPtr,
					StatusCode:     statusCodePtr,
					Error:          errPtr,
					DurationMs:     durationMsPtr,
				})
				if insertErr != nil && cfg.Logger != nil {
					cfg.Logger.Warn("Failed to record LLM request", "error", insertErr)
				}
			}()
		}
		httpc = llmhttp.NewClient(nil, recorder)
	} else {
		// Still use the custom transport for headers, just without recording
		httpc = llmhttp.NewClient(nil, nil)
	}

	for _, model := range All() {
		svc, err := model.Factory(cfg, httpc)
		if err != nil {
			// Model not available (e.g., missing API key) - skip it
			continue
		}
		manager.services[model.ID] = serviceEntry{
			service:  svc,
			provider: model.Provider,
			modelID:  model.ID,
		}
	}

	return manager, nil
}

// GetService returns the LLM service for the given model ID, wrapped with logging
func (m *Manager) GetService(modelID string) (llm.Service, error) {
	if entry, ok := m.services[modelID]; ok {
		// Wrap with logging if we have a logger
		if m.logger != nil {
			return &loggingService{
				service:  entry.service,
				logger:   m.logger,
				modelID:  entry.modelID,
				provider: entry.provider,
				db:       m.db,
			}, nil
		}
		return entry.service, nil
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
