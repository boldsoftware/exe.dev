package loop

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"shelley.exe.dev/llm"
)

func TestLoopWithClaudeTools(t *testing.T) {
	var recordedMessages []llm.Message

	recordFunc := func(ctx context.Context, message llm.Message, usage llm.Usage) error {
		recordedMessages = append(recordedMessages, message)
		return nil
	}

	// Use some actual claudetools
	tools := []*llm.Tool{
		// TODO: Add actual tools when needed
	}

	service := NewPredictableService()

	// Set up responses for a todo workflow
	todoWriteInput := json.RawMessage(`{"tasks": [{"id": "test-task", "task": "Complete the test", "status": "in-progress"}]}`)
	service.SetResponses([]PredictableResponse{
		{
			Content: "I'll create a todo list for you.",
			ToolCalls: []PredictableToolCall{
				{ID: "todo-1", Name: "todo_write", Input: todoWriteInput},
			},
			StopReason: llm.StopReasonToolUse,
			Usage:      llm.Usage{InputTokens: 15, OutputTokens: 8},
		},
		{
			Content:    "Great! I've created your todo list.",
			StopReason: llm.StopReasonStopSequence,
			Usage:      llm.Usage{InputTokens: 10, OutputTokens: 7},
		},
	})

	// Create loop with the configured service
	loop := NewLoop(Config{
		LLM:           service,
		History:       []llm.Message{},
		Tools:         tools,
		RecordMessage: recordFunc,
	})

	// Queue a user message
	userMessage := llm.Message{
		Role:    llm.MessageRoleUser,
		Content: []llm.Content{{Type: llm.ContentTypeText, Text: "Create a todo list"}},
	}
	loop.QueueUserMessage(userMessage)

	// Run the loop with a short timeout
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	err := loop.Go(ctx)
	if err != context.DeadlineExceeded {
		t.Errorf("expected context deadline exceeded, got %v", err)
	}

	// Verify that messages were recorded
	if len(recordedMessages) < 2 {
		t.Errorf("expected at least 2 recorded messages, got %d", len(recordedMessages))
	}

	// Check that usage was accumulated
	usage := loop.GetUsage()
	if usage.IsZero() {
		t.Error("expected non-zero usage")
	}

	if usage.InputTokens != 25 {
		t.Errorf("expected input tokens 25, got %d", usage.InputTokens)
	}

	if usage.OutputTokens != 15 {
		t.Errorf("expected output tokens 15, got %d", usage.OutputTokens)
	}

	// Verify conversation history includes all message types
	history := loop.GetHistory()
	if len(history) < 3 {
		t.Errorf("expected at least 3 history messages, got %d", len(history))
	}

	// Should have: user message, assistant message with tool_use, user message with tool_result, assistant message
	var hasToolUse, hasToolResult bool
	for _, msg := range history {
		for _, content := range msg.Content {
			if content.Type == llm.ContentTypeToolUse {
				hasToolUse = true
			}
			if content.Type == llm.ContentTypeToolResult {
				hasToolResult = true
			}
		}
	}

	if !hasToolUse {
		t.Error("expected history to contain tool_use content")
	}

	if !hasToolResult {
		t.Error("expected history to contain tool_result content")
	}
}

func TestLoopContextCancellation(t *testing.T) {
	service := NewPredictableService()
	service.AddSimpleResponse("Hello!")
	loop := NewLoop(Config{
		LLM:     service,
		History: []llm.Message{},
		Tools:   []*llm.Tool{},
		RecordMessage: func(ctx context.Context, message llm.Message, usage llm.Usage) error {
			return nil
		},
	})

	// Cancel context immediately
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := loop.Go(ctx)
	if err != context.Canceled {
		t.Errorf("expected context canceled, got %v", err)
	}
}

func TestLoopSystemMessages(t *testing.T) {
	// Set system messages
	system := []llm.SystemContent{
		{Text: "You are a helpful assistant.", Type: "text"},
	}

	loop := NewLoop(Config{
		LLM:     NewPredictableService(),
		History: []llm.Message{},
		Tools:   []*llm.Tool{},
		System:  system,
		RecordMessage: func(ctx context.Context, message llm.Message, usage llm.Usage) error {
			return nil
		},
	})

	// The system messages are stored and would be passed to LLM
	loop.mu.Lock()
	if len(loop.system) != 1 {
		t.Errorf("expected 1 system message, got %d", len(loop.system))
	}
	if loop.system[0].Text != "You are a helpful assistant." {
		t.Errorf("unexpected system message text: %s", loop.system[0].Text)
	}
	loop.mu.Unlock()
}
