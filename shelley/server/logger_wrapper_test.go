package server

import (
	"context"
	"log/slog"
	"testing"

	"shelley.exe.dev/llm"
	"shelley.exe.dev/loop"
)

// testHandler captures log output for testing
type testHandler struct {
	logs []slog.Record
}

func (h *testHandler) Enabled(context.Context, slog.Level) bool {
	return true
}

func (h *testHandler) Handle(_ context.Context, r slog.Record) error {
	h.logs = append(h.logs, r)
	return nil
}

func (h *testHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return h
}

func (h *testHandler) WithGroup(name string) slog.Handler {
	return h
}

func TestLoggingLLMService(t *testing.T) {
	// Create test handler to capture logs
	handler := &testHandler{}
	logger := slog.New(handler)

	// Create predictable service for testing
	predictableService := loop.NewPredictableService()

	// Wrap with logging
	loggedService := NewLoggingLLMService(predictableService, logger, "test-model")

	// Create a test request
	request := &llm.Request{
		Messages: []llm.Message{
			{
				Role: llm.MessageRoleUser,
				Content: []llm.Content{
					{Type: llm.ContentTypeText, Text: "Hello, world!"},
				},
			},
		},
	}

	// Make the request
	ctx := context.Background()
	response, err := loggedService.Do(ctx, request)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	// Verify response was returned
	if response == nil {
		t.Fatal("Response is nil")
	}

	// Verify logging occurred
	if len(handler.logs) == 0 {
		t.Fatal("No logs were captured")
	}

	// Find the completion log
	var completionLog *slog.Record
	for i := range handler.logs {
		if handler.logs[i].Message == "LLM request completed" {
			completionLog = &handler.logs[i]
			break
		}
	}

	if completionLog == nil {
		t.Fatal("No completion log found")
	}

	// Verify log attributes
	logAttrs := make(map[string]any)
	completionLog.Attrs(func(a slog.Attr) bool {
		logAttrs[a.Key] = a.Value.Any()
		return true
	})

	// Check required attributes
	if modelID, ok := logAttrs["model"]; !ok || modelID != "test-model" {
		t.Errorf("Expected model 'test-model', got %v", modelID)
	}

	if durationSeconds, ok := logAttrs["duration_seconds"]; !ok {
		t.Error("Expected duration_seconds attribute")
	} else if duration, ok := durationSeconds.(float64); !ok || duration <= 0 {
		t.Errorf("Expected positive duration, got %v", durationSeconds)
	}

	// Verify the service delegates other methods correctly
	if loggedService.TokenContextWindow() != predictableService.TokenContextWindow() {
		t.Error("TokenContextWindow delegation failed")
	}

	// Verify SimplifiedPatcher interface delegation
	if llm.UseSimplifiedPatch(loggedService) != llm.UseSimplifiedPatch(predictableService) {
		t.Error("SimplifiedPatcher delegation failed")
	}
}

func TestLoggingLLMServiceWithUsage(t *testing.T) {
	// Create test handler to capture logs
	handler := &testHandler{}
	logger := slog.New(handler)

	// Create a mock service that returns usage information
	mockService := &mockLLMService{
		response: &llm.Response{
			Content: []llm.Content{
				{Type: llm.ContentTypeText, Text: "Test response"},
			},
			Usage: llm.Usage{
				InputTokens:  100,
				OutputTokens: 50,
				CostUSD:      0.001,
			},
		},
	}

	// Wrap with logging
	loggedService := NewLoggingLLMService(mockService, logger, "test-model-with-usage")

	// Create a test request
	request := &llm.Request{
		Messages: []llm.Message{
			{
				Role: llm.MessageRoleUser,
				Content: []llm.Content{
					{Type: llm.ContentTypeText, Text: "Hello, world!"},
				},
			},
		},
	}

	// Make the request
	ctx := context.Background()
	_, err := loggedService.Do(ctx, request)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	// Find the completion log
	var completionLog *slog.Record
	for i := range handler.logs {
		if handler.logs[i].Message == "LLM request completed" {
			completionLog = &handler.logs[i]
			break
		}
	}

	if completionLog == nil {
		t.Fatal("No completion log found")
	}

	// Verify log attributes include usage information
	logAttrs := make(map[string]any)
	completionLog.Attrs(func(a slog.Attr) bool {
		logAttrs[a.Key] = a.Value.Any()
		return true
	})

	// Check usage attributes
	expectedAttrs := map[string]any{
		"model":         "test-model-with-usage",
		"input_tokens":  uint64(100),
		"output_tokens": uint64(50),
		"cost_usd":      0.001,
	}

	for key, expected := range expectedAttrs {
		if actual, ok := logAttrs[key]; !ok {
			t.Errorf("Expected attribute %s not found", key)
		} else if actual != expected {
			t.Errorf("Expected %s=%v, got %v", key, expected, actual)
		}
	}
}

func TestLoggingLLMServiceError(t *testing.T) {
	// Create test handler to capture logs
	handler := &testHandler{}
	logger := slog.New(handler)

	// Create a mock service that returns an error
	mockService := &mockLLMService{
		err: llm.ErrorfToolOut("test error").Error,
	}

	// Wrap with logging
	loggedService := NewLoggingLLMService(mockService, logger, "test-model-error")

	// Create a test request
	request := &llm.Request{
		Messages: []llm.Message{
			{
				Role: llm.MessageRoleUser,
				Content: []llm.Content{
					{Type: llm.ContentTypeText, Text: "Hello, world!"},
				},
			},
		},
	}

	// Make the request
	ctx := context.Background()
	_, err := loggedService.Do(ctx, request)
	if err == nil {
		t.Fatal("Expected error but got none")
	}

	// Find the error log
	var errorLog *slog.Record
	for i := range handler.logs {
		if handler.logs[i].Message == "LLM request failed" {
			errorLog = &handler.logs[i]
			break
		}
	}

	if errorLog == nil {
		t.Fatal("No error log found")
	}

	// Verify log attributes
	logAttrs := make(map[string]any)
	errorLog.Attrs(func(a slog.Attr) bool {
		logAttrs[a.Key] = a.Value.Any()
		return true
	})

	// Check required attributes
	if modelID, ok := logAttrs["model"]; !ok || modelID != "test-model-error" {
		t.Errorf("Expected model 'test-model-error', got %v", modelID)
	}

	if _, ok := logAttrs["duration_seconds"]; !ok {
		t.Error("Expected duration_seconds attribute")
	}

	if _, ok := logAttrs["error"]; !ok {
		t.Error("Expected error attribute")
	}
}

// mockLLMService for testing
type mockLLMService struct {
	response *llm.Response
	err      error
}

func (m *mockLLMService) Do(ctx context.Context, req *llm.Request) (*llm.Response, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.response, nil
}

func (m *mockLLMService) TokenContextWindow() int {
	return 4096
}
