package server

import (
	"context"
	"log/slog"
	"time"

	"shelley.exe.dev/llm"
)

// LoggingLLMService wraps an llm.Service to log request completion with usage information
type LoggingLLMService struct {
	service llm.Service
	logger  *slog.Logger
	modelID string
}

// NewLoggingLLMService creates a new logging wrapper around an LLM service
func NewLoggingLLMService(service llm.Service, logger *slog.Logger, modelID string) *LoggingLLMService {
	return &LoggingLLMService{
		service: service,
		logger:  logger,
		modelID: modelID,
	}
}

// Do wraps the underlying service's Do method with logging
func (l *LoggingLLMService) Do(ctx context.Context, request *llm.Request) (*llm.Response, error) {
	start := time.Now()

	// Call the underlying service
	response, err := l.service.Do(ctx, request)

	duration := time.Since(start)
	durationSeconds := duration.Seconds()

	// Log the completion with usage information
	if err != nil {
		l.logger.Error("LLM request failed",
			"model", l.modelID,
			"duration_seconds", durationSeconds,
			"error", err,
		)
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
func (l *LoggingLLMService) TokenContextWindow() int {
	return l.service.TokenContextWindow()
}

// Implement SimplifiedPatcher interface if the underlying service supports it
func (l *LoggingLLMService) UseSimplifiedPatch() bool {
	if sp, ok := l.service.(llm.SimplifiedPatcher); ok {
		return sp.UseSimplifiedPatch()
	}
	return false
}
