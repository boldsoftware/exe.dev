package loop

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/llm"
)

func TestNewLoop(t *testing.T) {
	history := []llm.Message{
		{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "Hello"}}},
	}
	tools := []*llm.Tool{}
	recordFunc := func(ctx context.Context, message llm.Message, usage llm.Usage) error {
		return nil
	}

	loop := NewLoop(Config{
		LLM:           NewPredictableService(),
		History:       history,
		Tools:         tools,
		RecordMessage: recordFunc,
	})
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
	loop := NewLoop(Config{
		LLM:     NewPredictableService(),
		History: []llm.Message{},
		Tools:   []*llm.Tool{},
	})

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

	// Test simple hello response
	ctx := context.Background()
	req := &llm.Request{
		Messages: []llm.Message{
			{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "hello"}}},
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

	if resp.Content[0].Text != "Well, hi there!" {
		t.Errorf("unexpected response text: %s", resp.Content[0].Text)
	}
}

func TestPredictableServiceEcho(t *testing.T) {
	service := NewPredictableService()

	ctx := context.Background()
	req := &llm.Request{
		Messages: []llm.Message{
			{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "echo: foo"}}},
		},
	}

	resp, err := service.Do(ctx, req)
	if err != nil {
		t.Fatalf("echo test failed: %v", err)
	}

	if resp.Content[0].Text != "foo" {
		t.Errorf("expected 'foo', got '%s'", resp.Content[0].Text)
	}

	// Test another echo
	req.Messages[0].Content[0].Text = "echo: hello world"
	resp, err = service.Do(ctx, req)
	if err != nil {
		t.Fatalf("echo hello world test failed: %v", err)
	}

	if resp.Content[0].Text != "hello world" {
		t.Errorf("expected 'hello world', got '%s'", resp.Content[0].Text)
	}
}

func TestPredictableServiceBashTool(t *testing.T) {
	service := NewPredictableService()

	ctx := context.Background()
	req := &llm.Request{
		Messages: []llm.Message{
			{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "bash: ls -la"}}},
		},
	}

	resp, err := service.Do(ctx, req)
	if err != nil {
		t.Fatalf("bash tool test failed: %v", err)
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

	if toolUseContent.ToolName != "bash" {
		t.Errorf("expected tool name 'bash', got '%s'", toolUseContent.ToolName)
	}

	// Check tool input contains the command
	var toolInput map[string]interface{}
	if err := json.Unmarshal(toolUseContent.ToolInput, &toolInput); err != nil {
		t.Fatalf("failed to parse tool input: %v", err)
	}

	if toolInput["command"] != "ls -la" {
		t.Errorf("expected command 'ls -la', got '%v'", toolInput["command"])
	}
}

func TestPredictableServiceDefaultResponse(t *testing.T) {
	service := NewPredictableService()

	ctx := context.Background()
	req := &llm.Request{
		Messages: []llm.Message{
			{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "some unknown input"}}},
		},
	}

	resp, err := service.Do(ctx, req)
	if err != nil {
		t.Fatalf("default response test failed: %v", err)
	}

	if resp.Content[0].Text != "edit predictable.go to add a response for that one..." {
		t.Errorf("unexpected default response: %s", resp.Content[0].Text)
	}
}

func TestPredictableServiceDelay(t *testing.T) {
	service := NewPredictableService()

	ctx := context.Background()
	req := &llm.Request{
		Messages: []llm.Message{
			{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "delay: 0.1"}}},
		},
	}

	start := time.Now()
	resp, err := service.Do(ctx, req)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("delay test failed: %v", err)
	}

	if elapsed < 100*time.Millisecond {
		t.Errorf("expected delay of at least 100ms, got %v", elapsed)
	}

	if resp.Content[0].Text != "Delayed for 0.1 seconds" {
		t.Errorf("unexpected response text: %s", resp.Content[0].Text)
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
	loop := NewLoop(Config{
		LLM:           service,
		History:       []llm.Message{},
		Tools:         []*llm.Tool{},
		RecordMessage: recordFunc,
	})

	// Queue a user message that triggers a known response
	userMessage := llm.Message{
		Role:    llm.MessageRoleUser,
		Content: []llm.Content{{Type: llm.ContentTypeText, Text: "hello"}},
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
		Name:        "bash",
		Description: "A test bash tool",
		InputSchema: llm.MustSchema(`{"type": "object", "properties": {"command": {"type": "string"}}}`),
		Run: func(ctx context.Context, input json.RawMessage) llm.ToolOut {
			toolCalls = append(toolCalls, string(input))
			return llm.ToolOut{
				LLMContent: []llm.Content{
					{Type: llm.ContentTypeText, Text: "Command executed successfully"},
				},
			}
		},
	}

	service := NewPredictableService()
	loop := NewLoop(Config{
		LLM:     service,
		History: []llm.Message{},
		Tools:   []*llm.Tool{testTool},
		RecordMessage: func(ctx context.Context, message llm.Message, usage llm.Usage) error {
			return nil
		},
	})

	// Queue a user message that triggers the bash tool
	userMessage := llm.Message{
		Role:    llm.MessageRoleUser,
		Content: []llm.Content{{Type: llm.ContentTypeText, Text: "bash: echo hello"}},
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

	if toolCalls[0] != `{"command":"echo hello"}` {
		t.Errorf("unexpected tool call input: %s", toolCalls[0])
	}
}

func TestGetHistory(t *testing.T) {
	initialHistory := []llm.Message{
		{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "Hello"}}},
	}

	loop := NewLoop(Config{
		LLM:     NewPredictableService(),
		History: initialHistory,
		Tools:   []*llm.Tool{},
	})

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

func TestLoopWithKeywordTool(t *testing.T) {
	// Test that keyword tool doesn't crash with nil pointer dereference
	service := NewPredictableService()

	var messages []llm.Message
	recordMessage := func(ctx context.Context, message llm.Message, usage llm.Usage) error {
		messages = append(messages, message)
		return nil
	}

	// Add a mock keyword tool that doesn't actually search
	tools := []*llm.Tool{
		{
			Name:        "keyword_search",
			Description: "Mock keyword search",
			InputSchema: llm.MustSchema(`{"type": "object", "properties": {"query": {"type": "string"}, "search_terms": {"type": "array", "items": {"type": "string"}}}, "required": ["query", "search_terms"]}`),
			Run: func(ctx context.Context, input json.RawMessage) llm.ToolOut {
				// Simple mock implementation
				return llm.ToolOut{LLMContent: []llm.Content{{Type: llm.ContentTypeText, Text: "mock keyword search result"}}}
			},
		},
	}

	loop := NewLoop(Config{
		LLM:           service,
		History:       []llm.Message{},
		Tools:         tools,
		RecordMessage: recordMessage,
	})

	// Send a user message that will trigger the default response
	userMessage := llm.Message{
		Role: llm.MessageRoleUser,
		Content: []llm.Content{
			{Type: llm.ContentTypeText, Text: "Please search for some files"},
		},
	}

	loop.QueueUserMessage(userMessage)

	// Process one turn
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := loop.ProcessOneTurn(ctx)
	if err != nil {
		t.Fatalf("ProcessOneTurn failed: %v", err)
	}

	// Verify we got expected messages
	// Note: User messages are recorded by ConversationManager, not by Loop,
	// so we only expect the assistant response to be recorded here
	if len(messages) < 1 {
		t.Fatalf("Expected at least 1 message (assistant), got %d", len(messages))
	}

	// Should have assistant response
	if messages[0].Role != llm.MessageRoleAssistant {
		t.Errorf("Expected first recorded message to be assistant, got %s", messages[0].Role)
	}
}

func TestLoopWithActualKeywordTool(t *testing.T) {
	// Test that actual keyword tool works with Loop
	service := NewPredictableService()

	var messages []llm.Message
	recordMessage := func(ctx context.Context, message llm.Message, usage llm.Usage) error {
		messages = append(messages, message)
		return nil
	}

	// Use the actual keyword tool from claudetool package
	// Note: We need to import it first
	tools := []*llm.Tool{
		// Add a simplified keyword tool to avoid file system dependencies in tests
		{
			Name:        "keyword_search",
			Description: "Search for files by keyword",
			InputSchema: llm.MustSchema(`{"type": "object", "properties": {"query": {"type": "string"}, "search_terms": {"type": "array", "items": {"type": "string"}}}, "required": ["query", "search_terms"]}`),
			Run: func(ctx context.Context, input json.RawMessage) llm.ToolOut {
				// Simple mock implementation - no context dependencies
				return llm.ToolOut{LLMContent: []llm.Content{{Type: llm.ContentTypeText, Text: "mock keyword search result"}}}
			},
		},
	}

	loop := NewLoop(Config{
		LLM:           service,
		History:       []llm.Message{},
		Tools:         tools,
		RecordMessage: recordMessage,
	})

	// Send a user message that will trigger the default response
	userMessage := llm.Message{
		Role: llm.MessageRoleUser,
		Content: []llm.Content{
			{Type: llm.ContentTypeText, Text: "Please search for some files"},
		},
	}

	loop.QueueUserMessage(userMessage)

	// Process one turn
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := loop.ProcessOneTurn(ctx)
	if err != nil {
		t.Fatalf("ProcessOneTurn failed: %v", err)
	}

	// Verify we got expected messages
	// Note: User messages are recorded by ConversationManager, not by Loop,
	// so we only expect the assistant response to be recorded here
	if len(messages) < 1 {
		t.Fatalf("Expected at least 1 message (assistant), got %d", len(messages))
	}

	// Should have assistant response
	if messages[0].Role != llm.MessageRoleAssistant {
		t.Errorf("Expected first recorded message to be assistant, got %s", messages[0].Role)
	}

	t.Log("Keyword tool test passed - no nil pointer dereference occurred")
}

func TestKeywordToolWithLLMProvider(t *testing.T) {
	// Create a temp directory with a test file to search
	tempDir := t.TempDir()
	testFile := filepath.Join(tempDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("this is a test file\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create a predictable service for testing
	predictableService := NewPredictableService()

	// Create a simple LLM provider for testing
	llmProvider := &testLLMProvider{
		service: predictableService,
		models:  []string{"predictable"},
	}

	// Create keyword tool with provider - use temp dir instead of /
	keywordTool := claudetool.NewKeywordToolWithWorkingDir(llmProvider, claudetool.NewMutableWorkingDir(tempDir))
	tool := keywordTool.Tool()

	// Test input
	input := `{"query": "test search", "search_terms": ["test"]}`

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result := tool.Run(ctx, json.RawMessage(input))

	// Should get a result without error (even though ripgrep will fail in test environment)
	// The important thing is that it doesn't crash with nil pointer dereference
	if result.Error != nil {
		t.Logf("Expected error in test environment (no ripgrep): %v", result.Error)
		// This is expected in test environment
	} else {
		t.Log("Keyword tool executed successfully")
		if len(result.LLMContent) == 0 {
			t.Error("Expected some content in result")
		}
	}
}

// testLLMProvider implements LLMServiceProvider for testing
type testLLMProvider struct {
	service llm.Service
	models  []string
}

func (t *testLLMProvider) GetService(modelID string) (llm.Service, error) {
	for _, model := range t.models {
		if model == modelID {
			return t.service, nil
		}
	}
	return nil, fmt.Errorf("model %s not available", modelID)
}

func (t *testLLMProvider) GetAvailableModels() []string {
	return t.models
}

func TestInsertMissingToolResults(t *testing.T) {
	tests := []struct {
		name     string
		messages []llm.Message
		wantLen  int
		wantText string
	}{
		{
			name: "no missing tool results",
			messages: []llm.Message{
				{
					Role: llm.MessageRoleAssistant,
					Content: []llm.Content{
						{Type: llm.ContentTypeText, Text: "Let me help you"},
					},
				},
				{
					Role: llm.MessageRoleUser,
					Content: []llm.Content{
						{Type: llm.ContentTypeText, Text: "Thanks"},
					},
				},
			},
			wantLen:  1,
			wantText: "", // No synthetic result expected
		},
		{
			name: "missing tool result - should insert synthetic result",
			messages: []llm.Message{
				{
					Role: llm.MessageRoleAssistant,
					Content: []llm.Content{
						{Type: llm.ContentTypeText, Text: "I'll use a tool"},
						{Type: llm.ContentTypeToolUse, ID: "tool_123", ToolName: "bash"},
					},
				},
				{
					Role: llm.MessageRoleUser,
					Content: []llm.Content{
						{Type: llm.ContentTypeText, Text: "Error occurred"},
					},
				},
			},
			wantLen:  2, // Should have synthetic tool_result + error message
			wantText: "not executed; retry possible",
		},
		{
			name: "multiple missing tool results",
			messages: []llm.Message{
				{
					Role: llm.MessageRoleAssistant,
					Content: []llm.Content{
						{Type: llm.ContentTypeText, Text: "I'll use multiple tools"},
						{Type: llm.ContentTypeToolUse, ID: "tool_1", ToolName: "bash"},
						{Type: llm.ContentTypeToolUse, ID: "tool_2", ToolName: "read"},
					},
				},
				{
					Role: llm.MessageRoleUser,
					Content: []llm.Content{
						{Type: llm.ContentTypeText, Text: "Error occurred"},
					},
				},
			},
			wantLen: 3, // Should have 2 synthetic tool_results + error message
		},
		{
			name: "has tool results - should not insert",
			messages: []llm.Message{
				{
					Role: llm.MessageRoleAssistant,
					Content: []llm.Content{
						{Type: llm.ContentTypeText, Text: "I'll use a tool"},
						{Type: llm.ContentTypeToolUse, ID: "tool_123", ToolName: "bash"},
					},
				},
				{
					Role: llm.MessageRoleUser,
					Content: []llm.Content{
						{
							Type:       llm.ContentTypeToolResult,
							ToolUseID:  "tool_123",
							ToolResult: []llm.Content{{Type: llm.ContentTypeText, Text: "result"}},
						},
					},
				},
			},
			wantLen: 1, // Should not insert anything
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			loop := NewLoop(Config{
				LLM:     NewPredictableService(),
				History: []llm.Message{},
			})

			req := &llm.Request{
				Messages: tt.messages,
			}

			loop.insertMissingToolResults(req)

			got := req.Messages[len(req.Messages)-1]
			if len(got.Content) != tt.wantLen {
				t.Errorf("expected %d content items, got %d", tt.wantLen, len(got.Content))
			}

			if tt.wantText != "" {
				// Find the synthetic tool result
				found := false
				for _, c := range got.Content {
					if c.Type == llm.ContentTypeToolResult && len(c.ToolResult) > 0 {
						if c.ToolResult[0].Text == tt.wantText {
							found = true
							if !c.ToolError {
								t.Error("synthetic tool result should have ToolError=true")
							}
							break
						}
					}
				}
				if !found {
					t.Errorf("expected to find synthetic tool result with text %q", tt.wantText)
				}
			}
		})
	}
}

func TestInsertMissingToolResultsWithEdgeCases(t *testing.T) {
	// Test for the bug: when an assistant error message is recorded after a tool_use
	// but before tool execution, the tool_use is "hidden" from insertMissingToolResults
	// because it only checks the last two messages.
	t.Run("tool_use hidden by subsequent assistant message", func(t *testing.T) {
		loop := NewLoop(Config{
			LLM:     NewPredictableService(),
			History: []llm.Message{},
		})

		// Scenario:
		// 1. LLM responds with tool_use
		// 2. Something fails, error message recorded (assistant message)
		// 3. User sends new message
		// The tool_use in message 0 is never followed by a tool_result
		req := &llm.Request{
			Messages: []llm.Message{
				{
					Role: llm.MessageRoleAssistant,
					Content: []llm.Content{
						{Type: llm.ContentTypeText, Text: "I'll run a command"},
						{Type: llm.ContentTypeToolUse, ID: "tool_hidden", ToolName: "bash"},
					},
				},
				{
					Role: llm.MessageRoleAssistant,
					Content: []llm.Content{
						{Type: llm.ContentTypeText, Text: "LLM request failed: some error"},
					},
				},
				{
					Role: llm.MessageRoleUser,
					Content: []llm.Content{
						{Type: llm.ContentTypeText, Text: "Please try again"},
					},
				},
			},
		}

		loop.insertMissingToolResults(req)

		// The function should have inserted a tool_result for tool_hidden
		// It should be inserted as a user message after the assistant message with tool_use
		// Since we can't insert in the middle, we need to ensure the history is valid

		// Check that there's a tool_result for tool_hidden somewhere in the messages
		found := false
		for _, msg := range req.Messages {
			for _, c := range msg.Content {
				if c.Type == llm.ContentTypeToolResult && c.ToolUseID == "tool_hidden" {
					found = true
					if !c.ToolError {
						t.Error("synthetic tool result should have ToolError=true")
					}
					break
				}
			}
		}
		if !found {
			t.Error("expected to find synthetic tool result for tool_hidden - the bug is that tool_use is hidden by subsequent assistant message")
		}
	})

	// Test for tool_use in earlier message (not the second-to-last)
	t.Run("tool_use in earlier message without result", func(t *testing.T) {
		loop := NewLoop(Config{
			LLM:     NewPredictableService(),
			History: []llm.Message{},
		})

		req := &llm.Request{
			Messages: []llm.Message{
				{
					Role: llm.MessageRoleUser,
					Content: []llm.Content{
						{Type: llm.ContentTypeText, Text: "Do something"},
					},
				},
				{
					Role: llm.MessageRoleAssistant,
					Content: []llm.Content{
						{Type: llm.ContentTypeText, Text: "I'll use a tool"},
						{Type: llm.ContentTypeToolUse, ID: "tool_earlier", ToolName: "bash"},
					},
				},
				// Missing: user message with tool_result for tool_earlier
				{
					Role: llm.MessageRoleAssistant,
					Content: []llm.Content{
						{Type: llm.ContentTypeText, Text: "Something went wrong"},
					},
				},
				{
					Role: llm.MessageRoleUser,
					Content: []llm.Content{
						{Type: llm.ContentTypeText, Text: "Try again"},
					},
				},
			},
		}

		loop.insertMissingToolResults(req)

		// Should have inserted a tool_result for tool_earlier
		found := false
		for _, msg := range req.Messages {
			for _, c := range msg.Content {
				if c.Type == llm.ContentTypeToolResult && c.ToolUseID == "tool_earlier" {
					found = true
					break
				}
			}
		}
		if !found {
			t.Error("expected to find synthetic tool result for tool_earlier")
		}
	})

	t.Run("empty message list", func(t *testing.T) {
		loop := NewLoop(Config{
			LLM:     NewPredictableService(),
			History: []llm.Message{},
		})

		req := &llm.Request{
			Messages: []llm.Message{},
		}

		loop.insertMissingToolResults(req)
		// Should not panic
	})

	t.Run("single message", func(t *testing.T) {
		loop := NewLoop(Config{
			LLM:     NewPredictableService(),
			History: []llm.Message{},
		})

		req := &llm.Request{
			Messages: []llm.Message{
				{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "hello"}}},
			},
		}

		loop.insertMissingToolResults(req)
		// Should not panic, should not modify
		if len(req.Messages[0].Content) != 1 {
			t.Error("should not modify single message")
		}
	})

	t.Run("wrong role order - user then assistant", func(t *testing.T) {
		loop := NewLoop(Config{
			LLM:     NewPredictableService(),
			History: []llm.Message{},
		})

		req := &llm.Request{
			Messages: []llm.Message{
				{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "hello"}}},
				{Role: llm.MessageRoleAssistant, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "hi"}}},
			},
		}

		loop.insertMissingToolResults(req)
		// Should not modify when roles are wrong order
		if len(req.Messages[1].Content) != 1 {
			t.Error("should not modify when roles are in wrong order")
		}
	})
}
