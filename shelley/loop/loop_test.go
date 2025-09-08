package loop

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"sketch.dev/llm"
)

func TestNewLoop(t *testing.T) {
	history := []llm.Message{
		{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "Hello"}}},
	}
	tools := []*llm.Tool{}
	recordFunc := func(ctx context.Context, message llm.Message, usage llm.Usage) error {
		return nil
	}

	loop := NewLoop(NewPredictableService(), history, tools, recordFunc)
	if loop == nil {
		t.Fatal("NewLoop returned nil")
	}

	if len(loop.history) != 1 {
		t.Errorf("expected history length 1, got %d", len(loop.history))
	}

	if len(loop.messageQueue) != 0 {
		t.Errorf("expected empty message queue, got %d", len(loop.messageQueue))
	}
}

func TestQueueUserMessage(t *testing.T) {
	loop := NewLoop(NewPredictableService(), []llm.Message{}, []*llm.Tool{}, nil)

	message := llm.Message{
		Role:    llm.MessageRoleUser,
		Content: []llm.Content{{Type: llm.ContentTypeText, Text: "Test message"}},
	}

	loop.QueueUserMessage(message)

	loop.mu.Lock()
	queueLen := len(loop.messageQueue)
	loop.mu.Unlock()

	if queueLen != 1 {
		t.Errorf("expected message queue length 1, got %d", queueLen)
	}
}

func TestPredictableService(t *testing.T) {
	service := NewPredictableService()

	// Test default response
	ctx := context.Background()
	req := &llm.Request{
		Messages: []llm.Message{
			{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "Hello"}}},
		},
	}

	resp, err := service.Do(ctx, req)
	if err != nil {
		t.Fatalf("predictable service Do failed: %v", err)
	}

	if resp.Role != llm.MessageRoleAssistant {
		t.Errorf("expected assistant role, got %v", resp.Role)
	}

	if len(resp.Content) == 0 {
		t.Error("expected non-empty content")
	}

	if resp.Content[0].Type != llm.ContentTypeText {
		t.Errorf("expected text content, got %v", resp.Content[0].Type)
	}

	if resp.Content[0].Text != "Hello! I'm a predictable AI assistant. How can I help you today?" {
		t.Errorf("unexpected response text: %s", resp.Content[0].Text)
	}
}

func TestPredictableServiceCustomResponses(t *testing.T) {
	service := NewPredictableService()

	// Add custom responses
	service.SetResponses([]PredictableResponse{
		{
			Content:    "First response",
			StopReason: llm.StopReasonStopSequence,
			Usage:      llm.Usage{InputTokens: 5, OutputTokens: 3},
		},
		{
			Content:    "Second response",
			StopReason: llm.StopReasonStopSequence,
			Usage:      llm.Usage{InputTokens: 5, OutputTokens: 3},
		},
	})

	ctx := context.Background()
	req := &llm.Request{}

	// First call
	resp1, err := service.Do(ctx, req)
	if err != nil {
		t.Fatalf("first call failed: %v", err)
	}
	if resp1.Content[0].Text != "First response" {
		t.Errorf("expected 'First response', got '%s'", resp1.Content[0].Text)
	}

	// Second call
	resp2, err := service.Do(ctx, req)
	if err != nil {
		t.Fatalf("second call failed: %v", err)
	}
	if resp2.Content[0].Text != "Second response" {
		t.Errorf("expected 'Second response', got '%s'", resp2.Content[0].Text)
	}

	// Third call should return default response
	resp3, err := service.Do(ctx, req)
	if err != nil {
		t.Fatalf("third call failed: %v", err)
	}
	if resp3.Content[0].Text != "I've run out of predictable responses. Try special commands like 'echo foo', 'error bar', or 'tool bash ls'." {
		t.Errorf("expected default response, got '%s'", resp3.Content[0].Text)
	}
}

func TestPredictableServiceWithToolCalls(t *testing.T) {
	service := NewPredictableService()

	toolInput := json.RawMessage(`{"query": "test"}`)
	service.SetResponses([]PredictableResponse{
		{
			Content: "I'll use a tool to help with that.",
			ToolCalls: []PredictableToolCall{
				{
					ID:    "tool-123",
					Name:  "test_tool",
					Input: toolInput,
				},
			},
			StopReason: llm.StopReasonToolUse,
		},
	})

	ctx := context.Background()
	req := &llm.Request{}

	resp, err := service.Do(ctx, req)
	if err != nil {
		t.Fatalf("tool call response failed: %v", err)
	}

	if resp.StopReason != llm.StopReasonToolUse {
		t.Errorf("expected tool use stop reason, got %v", resp.StopReason)
	}

	if len(resp.Content) != 2 {
		t.Errorf("expected 2 content items (text + tool_use), got %d", len(resp.Content))
	}

	// Find the tool use content
	var toolUseContent *llm.Content
	for _, content := range resp.Content {
		if content.Type == llm.ContentTypeToolUse {
			toolUseContent = &content
			break
		}
	}

	if toolUseContent == nil {
		t.Fatal("no tool use content found")
	}

	if toolUseContent.ToolName != "test_tool" {
		t.Errorf("expected tool name 'test_tool', got '%s'", toolUseContent.ToolName)
	}

	if toolUseContent.ID != "tool-123" {
		t.Errorf("expected tool ID 'tool-123', got '%s'", toolUseContent.ID)
	}
}

func TestLoopWithPredictableService(t *testing.T) {
	var recordedMessages []llm.Message
	var recordedUsages []llm.Usage

	recordFunc := func(ctx context.Context, message llm.Message, usage llm.Usage) error {
		recordedMessages = append(recordedMessages, message)
		recordedUsages = append(recordedUsages, usage)
		return nil
	}

	service := NewPredictableService()
	service.AddSimpleResponse("Hello there!")
	loop := NewLoop(service, []llm.Message{}, []*llm.Tool{}, recordFunc)

	// Queue a user message
	userMessage := llm.Message{
		Role:    llm.MessageRoleUser,
		Content: []llm.Content{{Type: llm.ContentTypeText, Text: "Hello"}},
	}
	loop.QueueUserMessage(userMessage)

	// Run the loop with a short timeout
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := loop.Go(ctx)
	if err != context.DeadlineExceeded {
		t.Errorf("expected context deadline exceeded, got %v", err)
	}

	// Check that messages were recorded
	if len(recordedMessages) < 1 {
		t.Errorf("expected at least 1 recorded message, got %d", len(recordedMessages))
	}

	// Check usage tracking
	usage := loop.GetUsage()
	if usage.IsZero() {
		t.Error("expected non-zero usage")
	}
}

func TestLoopWithTools(t *testing.T) {
	var toolCalls []string

	testTool := &llm.Tool{
		Name:        "test_tool",
		Description: "A test tool",
		InputSchema: llm.MustSchema(`{"type": "object", "properties": {"input": {"type": "string"}}}`),
		Run: func(ctx context.Context, input json.RawMessage) llm.ToolOut {
			toolCalls = append(toolCalls, string(input))
			return llm.ToolOut{
				LLMContent: []llm.Content{
					{Type: llm.ContentTypeText, Text: "Tool executed successfully"},
				},
			}
		},
	}

	loop := NewLoop(NewPredictableService(), []llm.Message{}, []*llm.Tool{testTool}, func(ctx context.Context, message llm.Message, usage llm.Usage) error {
		return nil
	})

	service := NewPredictableService()
	toolInput := json.RawMessage(`{"input": "test data"}`)
	service.SetResponses([]PredictableResponse{
		{
			Content: "I'll use the test tool.",
			ToolCalls: []PredictableToolCall{
				{ID: "tool-1", Name: "test_tool", Input: toolInput},
			},
			StopReason: llm.StopReasonToolUse,
		},
		{
			Content:    "Tool completed successfully.",
			StopReason: llm.StopReasonStopSequence,
		},
	})

	loop.SetLLM(service)

	// Queue a user message
	userMessage := llm.Message{
		Role:    llm.MessageRoleUser,
		Content: []llm.Content{{Type: llm.ContentTypeText, Text: "Use the test tool"}},
	}
	loop.QueueUserMessage(userMessage)

	// Run the loop with a short timeout
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	err := loop.Go(ctx)
	if err != context.DeadlineExceeded {
		t.Errorf("expected context deadline exceeded, got %v", err)
	}

	// Check that the tool was called
	if len(toolCalls) != 1 {
		t.Errorf("expected 1 tool call, got %d", len(toolCalls))
	}

	if toolCalls[0] != `{"input": "test data"}` {
		t.Errorf("unexpected tool call input: %s", toolCalls[0])
	}
}

func TestGetHistory(t *testing.T) {
	initialHistory := []llm.Message{
		{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "Hello"}}},
	}

	loop := NewLoop(NewPredictableService(), initialHistory, []*llm.Tool{}, nil)

	history := loop.GetHistory()
	if len(history) != 1 {
		t.Errorf("expected history length 1, got %d", len(history))
	}

	// Modify returned slice to ensure it's a copy
	history[0].Content[0].Text = "Modified"

	// Original should be unchanged
	original := loop.GetHistory()
	if original[0].Content[0].Text != "Hello" {
		t.Error("GetHistory should return a copy, not the original slice")
	}
}
