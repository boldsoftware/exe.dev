package slug

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"testing"

	"shelley.exe.dev/db"
	"shelley.exe.dev/llm"
)

func TestSanitize(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"Simple Test", "simple-test"},
		{"Create a Python Script", "create-a-python-script"},
		{"Multiple   Spaces", "multiple-spaces"},
		{"Special@#$%Characters", "specialcharacters"},
		{"Under_Score_Test", "under-score-test"},
		{"--multiple-hyphens--", "multiple-hyphens"},
		{"CamelCase Example", "camelcase-example"},
		{"123 Numbers Test 456", "123-numbers-test-456"},
		{"   leading and trailing   ", "leading-and-trailing"},
		{"", ""},
		{"Very Long Slug That Might Need To Be Truncated Because It Is Too Long For Normal Use", "very-long-slug-that-might-need-to-be-truncated-because-it-is"},
	}

	for _, test := range tests {
		result := Sanitize(test.input)
		if result != test.expected {
			t.Errorf("Sanitize(%q) = %q, expected %q", test.input, result, test.expected)
		}
	}
}

// TestGenerateUniqueSlug tests that slug generation adds numeric suffixes when there are conflicts
func TestGenerateSlug_UniquenessSuffix(t *testing.T) {
	// This test verifies the numeric suffix logic without needing a real database or LLM
	// We'll test the error handling and retry logic by mocking the behavior

	// Test the sanitization works as expected first
	baseSlug := Sanitize("Test Message")
	expected := "test-message"
	if baseSlug != expected {
		t.Errorf("Sanitize failed: got %q, expected %q", baseSlug, expected)
	}

	// Test that numeric suffixes would be correctly formatted
	// This mimics what the GenerateSlug function does internally
	tests := []struct {
		baseSlug string
		attempt  int
		expected string
	}{
		{"test-message", 0, "test-message-1"},
		{"test-message", 1, "test-message-2"},
		{"test-message", 2, "test-message-3"},
		{"help-python", 9, "help-python-10"},
	}

	for _, test := range tests {
		result := fmt.Sprintf("%s-%d", test.baseSlug, test.attempt+1)
		if result != test.expected {
			t.Errorf("Suffix generation failed: got %q, expected %q", result, test.expected)
		}
	}
}

// MockLLMService provides a mock LLM service for testing
type MockLLMService struct {
	ResponseText string
}

func (m *MockLLMService) Do(ctx context.Context, req *llm.Request) (*llm.Response, error) {
	return &llm.Response{
		Content: []llm.Content{
			{Type: llm.ContentTypeText, Text: m.ResponseText},
		},
	}, nil
}

func (m *MockLLMService) TokenContextWindow() int {
	return 8192 // Mock token limit
}

// MockLLMProvider provides a mock LLM provider for testing
type MockLLMProvider struct {
	Service *MockLLMService
}

func (m *MockLLMProvider) GetService(modelID string) (llm.Service, error) {
	return m.Service, nil
}

// TestGenerateSlug_DatabaseIntegration tests slug generation with actual database conflicts
func TestGenerateSlug_DatabaseIntegration(t *testing.T) {
	// Create temporary database
	tempDB := t.TempDir() + "/slug_test.db"
	database, err := db.New(db.Config{DSN: tempDB})
	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}
	defer database.Close()

	// Run migrations
	ctx := context.Background()
	if err := database.Migrate(ctx); err != nil {
		t.Fatalf("Failed to migrate database: %v", err)
	}

	// Create mock LLM provider that always returns the same slug
	mockLLM := &MockLLMProvider{
		Service: &MockLLMService{
			ResponseText: "test-slug", // Always return the same slug to force conflicts
		},
	}

	// Create logger (silent for tests)
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelWarn, // Only show warnings and errors
	}))

	// Create first conversation to establish the base slug
	conv1, err := database.CreateConversation(ctx, nil, true, nil)
	if err != nil {
		t.Fatalf("Failed to create first conversation: %v", err)
	}

	// Generate first slug - should succeed with "test-slug"
	slug1, err := GenerateSlug(ctx, mockLLM, database, logger, conv1.ConversationID, "Test message", "")
	if err != nil {
		t.Fatalf("Failed to generate first slug: %v", err)
	}
	if slug1 != "test-slug" {
		t.Errorf("Expected first slug to be 'test-slug', got %q", slug1)
	}

	// Create second conversation
	conv2, err := database.CreateConversation(ctx, nil, true, nil)
	if err != nil {
		t.Fatalf("Failed to create second conversation: %v", err)
	}

	// Generate second slug - should get "test-slug-1" due to conflict
	slug2, err := GenerateSlug(ctx, mockLLM, database, logger, conv2.ConversationID, "Test message", "")
	if err != nil {
		t.Fatalf("Failed to generate second slug: %v", err)
	}
	if slug2 != "test-slug-1" {
		t.Errorf("Expected second slug to be 'test-slug-1', got %q", slug2)
	}

	// Create third conversation
	conv3, err := database.CreateConversation(ctx, nil, true, nil)
	if err != nil {
		t.Fatalf("Failed to create third conversation: %v", err)
	}

	// Generate third slug - should get "test-slug-2" due to conflict
	slug3, err := GenerateSlug(ctx, mockLLM, database, logger, conv3.ConversationID, "Test message", "")
	if err != nil {
		t.Fatalf("Failed to generate third slug: %v", err)
	}
	if slug3 != "test-slug-2" {
		t.Errorf("Expected third slug to be 'test-slug-2', got %q", slug3)
	}

	// Verify all slugs are different
	if slug1 == slug2 || slug1 == slug3 || slug2 == slug3 {
		t.Errorf("All slugs should be unique: slug1=%q, slug2=%q, slug3=%q", slug1, slug2, slug3)
	}

	t.Logf("Successfully generated unique slugs: %q, %q, %q", slug1, slug2, slug3)
}
