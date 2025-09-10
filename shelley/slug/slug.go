package slug

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"shelley.exe.dev/db"
	"shelley.exe.dev/llm"
)

// LLMServiceProvider defines the interface for getting LLM services
type LLMServiceProvider interface {
	GetService(modelID string) (llm.Service, error)
}

// GenerateSlug generates a slug for a conversation and updates the database
func GenerateSlug(ctx context.Context, llmProvider LLMServiceProvider, database *db.DB, logger *slog.Logger, conversationID, userMessage string) (string, error) {
	slug, err := generateSlugText(ctx, llmProvider, logger, userMessage)
	if err != nil {
		return "", err
	}

	// Update conversation with the generated slug
	_, err = database.UpdateConversationSlug(ctx, conversationID, slug)
	if err != nil {
		return "", fmt.Errorf("failed to update conversation slug: %w", err)
	}

	logger.Info("Generated slug for conversation", "conversationID", conversationID, "slug", slug)
	return slug, nil
}

// generateSlugText generates a human-readable slug for a conversation based on the user message
func generateSlugText(ctx context.Context, llmProvider LLMServiceProvider, logger *slog.Logger, userMessage string) (string, error) {
	// Try different models in order of preference
	var llmService llm.Service
	var err error

	// Preferred models in order of preference
	preferredModels := []string{"gpt5-mini", "gpt-5-thinking-mini", "claude-sonnet-3.5", "qwen3-coder-fireworks", "predictable"}

	for _, model := range preferredModels {
		llmService, err = llmProvider.GetService(model)
		if err == nil {
			break
		}
		logger.Debug("Model not available for slug generation", "model", model, "error", err)
	}

	if llmService == nil {
		return "", fmt.Errorf("no suitable model available for slug generation")
	}

	// Create a focused prompt for slug generation
	slugPrompt := fmt.Sprintf(`Generate a short, descriptive slug (2-6 words, lowercase, hyphen-separated) for a conversation that starts with this user message:

%s

The slug should:
- Be concise and descriptive
- Use only lowercase letters, numbers, and hyphens
- Capture the main topic or intent
- Be suitable as a filename or URL path

Respond with only the slug, nothing else.`, userMessage)

	message := llm.Message{
		Role: llm.MessageRoleUser,
		Content: []llm.Content{
			{Type: llm.ContentTypeText, Text: slugPrompt},
		},
	}

	request := &llm.Request{
		Messages: []llm.Message{message},
	}

	// Make LLM request with timeout
	ctxWithTimeout, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	response, err := llmService.Do(ctxWithTimeout, request)
	if err != nil {
		return "", fmt.Errorf("failed to generate slug: %w", err)
	}

	// Extract text from response
	if len(response.Content) == 0 {
		return "", fmt.Errorf("empty response from LLM")
	}

	slug := strings.TrimSpace(response.Content[0].Text)

	// Clean and validate the slug
	slug = Sanitize(slug)
	if slug == "" {
		return "", fmt.Errorf("generated slug is empty after sanitization")
	}

	// Note: We don't check for uniqueness here since we're generating for a new conversation
	// and the database will handle any conflicts

	return slug, nil
}

// Sanitize cleans a string to be a valid slug
func Sanitize(input string) string {
	// Convert to lowercase
	slug := strings.ToLower(input)

	// Replace spaces and underscores with hyphens
	slug = regexp.MustCompile(`[\s_]+`).ReplaceAllString(slug, "-")

	// Remove non-alphanumeric characters except hyphens
	slug = regexp.MustCompile(`[^a-z0-9-]+`).ReplaceAllString(slug, "")

	// Remove multiple consecutive hyphens
	slug = regexp.MustCompile(`-+`).ReplaceAllString(slug, "-")

	// Remove leading/trailing hyphens
	slug = strings.Trim(slug, "-")

	// Limit length
	if len(slug) > 60 {
		slug = slug[:60]
		slug = strings.Trim(slug, "-")
	}

	return slug
}
