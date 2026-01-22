package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/db"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/loop"
)

// TestOrphanToolResultAfterCancellation reproduces the bug where a tool_result
// is written after CancelConversation has already written an end-turn message.
//
// This leads to the Anthropic API error:
// "unexpected `tool_use_id` found in `tool_result` blocks: <id>.
// Each `tool_result` block must have a corresponding `tool_use` block in the previous message."
//
// The sequence is:
// 1. LLM returns assistant message with tool_use X
// 2. Tool X starts executing
// 3. User cancels
// 4. CancelConversation writes:
//   - user message with cancelled tool_result X
//   - assistant message with end-turn "[Operation cancelled]"
//
// 5. Tool X completes and writes its result AFTER the cancel messages
// 6. DB now has:
//   - assistant with tool_use X
//   - user with tool_result X (cancelled)
//   - assistant end-turn
//   - user with tool_result X (actual) <- ORPHAN - references X but previous msg has no tool_use!
func TestOrphanToolResultAfterCancellation(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	predictableService := loop.NewPredictableService()
	llmManager := &testLLMManager{service: predictableService}
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelWarn}))

	toolSetConfig := claudetool.ToolSetConfig{EnableBrowser: false}
	server := NewServer(database, llmManager, toolSetConfig, logger, true, "", "predictable", "", nil)

	// Create conversation
	conversation, err := database.CreateConversation(context.Background(), nil, true, nil, nil)
	if err != nil {
		t.Fatalf("failed to create conversation: %v", err)
	}
	conversationID := conversation.ConversationID

	// Manually create the problematic message sequence in the database
	// This simulates the race condition where a tool result is written after cancellation

	toolUseID := "toolu_test_orphan_12345"

	// Message 1: User message "run something"
	userMsg1 := llm.Message{
		Role: llm.MessageRoleUser,
		Content: []llm.Content{
			{Type: llm.ContentTypeText, Text: "bash: echo hello"},
		},
	}
	if _, err := database.CreateMessage(context.Background(), db.CreateMessageParams{
		ConversationID: conversationID,
		Type:           db.MessageTypeUser,
		LLMData:        userMsg1,
		UsageData:      llm.Usage{},
	}); err != nil {
		t.Fatalf("failed to create user message: %v", err)
	}

	// Message 2: Assistant message with tool_use
	assistantMsg1 := llm.Message{
		Role: llm.MessageRoleAssistant,
		Content: []llm.Content{
			{Type: llm.ContentTypeText, Text: "I'll run the command"},
			{
				ID:        toolUseID,
				Type:      llm.ContentTypeToolUse,
				ToolName:  "bash",
				ToolInput: json.RawMessage(`{"command": "echo hello"}`),
			},
		},
	}
	if _, err := database.CreateMessage(context.Background(), db.CreateMessageParams{
		ConversationID: conversationID,
		Type:           db.MessageTypeAgent,
		LLMData:        assistantMsg1,
		UsageData:      llm.Usage{},
	}); err != nil {
		t.Fatalf("failed to create assistant message: %v", err)
	}

	// Message 3: User message with cancelled tool_result (from CancelConversation)
	now := time.Now()
	cancelledToolResult := llm.Message{
		Role: llm.MessageRoleUser,
		Content: []llm.Content{
			{
				Type:             llm.ContentTypeToolResult,
				ToolUseID:        toolUseID,
				ToolError:        true,
				ToolResult:       []llm.Content{{Type: llm.ContentTypeText, Text: "Tool execution cancelled by user"}},
				ToolUseStartTime: &now,
				ToolUseEndTime:   &now,
			},
		},
	}
	if _, err := database.CreateMessage(context.Background(), db.CreateMessageParams{
		ConversationID: conversationID,
		Type:           db.MessageTypeUser,
		LLMData:        cancelledToolResult,
		UsageData:      llm.Usage{},
	}); err != nil {
		t.Fatalf("failed to create cancelled tool_result message: %v", err)
	}

	// Message 4: Assistant end-turn message (from CancelConversation)
	endTurnMsg := llm.Message{
		Role:      llm.MessageRoleAssistant,
		Content:   []llm.Content{{Type: llm.ContentTypeText, Text: "[Operation cancelled]"}},
		EndOfTurn: true,
	}
	if _, err := database.CreateMessage(context.Background(), db.CreateMessageParams{
		ConversationID: conversationID,
		Type:           db.MessageTypeAgent,
		LLMData:        endTurnMsg,
		UsageData:      llm.Usage{},
	}); err != nil {
		t.Fatalf("failed to create end-turn message: %v", err)
	}

	// Message 5: ORPHAN - User message with actual tool_result (written after cancel due to race)
	// This references the tool_use from message 2, but the previous message (4) has no tool_use!
	actualToolResult := llm.Message{
		Role: llm.MessageRoleUser,
		Content: []llm.Content{
			{
				Type:             llm.ContentTypeToolResult,
				ToolUseID:        toolUseID,
				ToolError:        false,
				ToolResult:       []llm.Content{{Type: llm.ContentTypeText, Text: "hello\n"}},
				ToolUseStartTime: &now,
				ToolUseEndTime:   &now,
			},
		},
	}
	if _, err := database.CreateMessage(context.Background(), db.CreateMessageParams{
		ConversationID: conversationID,
		Type:           db.MessageTypeUser,
		LLMData:        actualToolResult,
		UsageData:      llm.Usage{},
	}); err != nil {
		t.Fatalf("failed to create orphan tool_result message: %v", err)
	}

	// Now try to resume the conversation
	// This should trigger the Anthropic API error if we don't fix the orphan tool_result
	resumeReq := ChatRequest{
		Message: "echo: continue",
		Model:   "predictable",
	}
	resumeBody, _ := json.Marshal(resumeReq)

	req := httptest.NewRequest("POST", "/api/conversation/"+conversationID+"/chat", strings.NewReader(string(resumeBody)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	server.handleChatConversation(w, req, conversationID)
	if w.Code != http.StatusAccepted {
		t.Fatalf("expected status 202, got %d: %s", w.Code, w.Body.String())
	}

	// Wait for the request to be processed
	time.Sleep(300 * time.Millisecond)

	// Check the last request sent to the LLM for orphan tool_results
	lastRequest := predictableService.GetLastRequest()
	if lastRequest == nil {
		t.Fatal("no request was sent to the LLM")
	}

	// Check that orphan tool_results have been removed
	// An orphan tool_result is one that references a tool_use_id that doesn't exist
	// in the immediately preceding assistant message

	var previousAssistantToolUses map[string]bool
	for i, msg := range lastRequest.Messages {
		if msg.Role == llm.MessageRoleAssistant {
			// Track all tool_use IDs in this assistant message
			previousAssistantToolUses = make(map[string]bool)
			for _, content := range msg.Content {
				if content.Type == llm.ContentTypeToolUse {
					previousAssistantToolUses[content.ID] = true
				}
			}
		} else if msg.Role == llm.MessageRoleUser {
			// Check if any tool_results reference IDs not in previous assistant message
			for _, content := range msg.Content {
				if content.Type == llm.ContentTypeToolResult {
					if previousAssistantToolUses != nil && !previousAssistantToolUses[content.ToolUseID] {
						t.Errorf("BUG: Found orphan tool_result at message index %d with ToolUseID=%s that doesn't match any tool_use in the previous assistant message. "+
							"This would cause Anthropic API error: 'Each tool_result block must have a corresponding tool_use block in the previous message'",
							i, content.ToolUseID)
					}
				}
			}
			// Clear previousAssistantToolUses since user messages reset the expectation
			previousAssistantToolUses = nil
		}
	}

	t.Logf("LLM request has %d messages - test verified orphan tool_results are handled", len(lastRequest.Messages))
}

// TestOrphanToolResultFiltering tests that orphan tool_results are filtered out
// even when they appear in the middle of the conversation
func TestOrphanToolResultFiltering(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	predictableService := loop.NewPredictableService()
	llmManager := &testLLMManager{service: predictableService}
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelWarn}))

	server := NewServer(database, llmManager, claudetool.ToolSetConfig{}, logger, true, "", "predictable", "", nil)

	conversation, err := database.CreateConversation(context.Background(), nil, true, nil, nil)
	if err != nil {
		t.Fatalf("failed to create conversation: %v", err)
	}
	conversationID := conversation.ConversationID

	// Create a conversation where there's an orphan tool_result in the middle
	// followed by valid messages

	// Message 1: User message
	userMsg1 := llm.Message{
		Role:    llm.MessageRoleUser,
		Content: []llm.Content{{Type: llm.ContentTypeText, Text: "hello"}},
	}
	if _, err := database.CreateMessage(context.Background(), db.CreateMessageParams{
		ConversationID: conversationID,
		Type:           db.MessageTypeUser,
		LLMData:        userMsg1,
	}); err != nil {
		t.Fatalf("failed to create message: %v", err)
	}

	// Message 2: Assistant response with end_of_turn (no tool_use)
	assistantMsg := llm.Message{
		Role:      llm.MessageRoleAssistant,
		Content:   []llm.Content{{Type: llm.ContentTypeText, Text: "Hi there!"}},
		EndOfTurn: true,
	}
	if _, err := database.CreateMessage(context.Background(), db.CreateMessageParams{
		ConversationID: conversationID,
		Type:           db.MessageTypeAgent,
		LLMData:        assistantMsg,
	}); err != nil {
		t.Fatalf("failed to create message: %v", err)
	}

	// Message 3: ORPHAN tool_result - previous assistant has no tool_use!
	now := time.Now()
	orphanResult := llm.Message{
		Role: llm.MessageRoleUser,
		Content: []llm.Content{
			{
				Type:             llm.ContentTypeToolResult,
				ToolUseID:        "toolu_orphan_xyz",
				ToolError:        false,
				ToolResult:       []llm.Content{{Type: llm.ContentTypeText, Text: "orphan result"}},
				ToolUseStartTime: &now,
				ToolUseEndTime:   &now,
			},
		},
	}
	if _, err := database.CreateMessage(context.Background(), db.CreateMessageParams{
		ConversationID: conversationID,
		Type:           db.MessageTypeUser,
		LLMData:        orphanResult,
	}); err != nil {
		t.Fatalf("failed to create orphan message: %v", err)
	}

	// Now try to chat
	chatReq := ChatRequest{
		Message: "echo: test",
		Model:   "predictable",
	}
	chatBody, _ := json.Marshal(chatReq)

	req := httptest.NewRequest("POST", "/api/conversation/"+conversationID+"/chat", strings.NewReader(string(chatBody)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	server.handleChatConversation(w, req, conversationID)
	if w.Code != http.StatusAccepted {
		t.Fatalf("expected status 202, got %d: %s", w.Code, w.Body.String())
	}

	time.Sleep(300 * time.Millisecond)

	lastRequest := predictableService.GetLastRequest()
	if lastRequest == nil {
		t.Fatal("no request was sent to the LLM")
	}

	// Verify no orphan tool_results in the request
	var prevToolUses map[string]bool
	for i, msg := range lastRequest.Messages {
		if msg.Role == llm.MessageRoleAssistant {
			prevToolUses = make(map[string]bool)
			for _, content := range msg.Content {
				if content.Type == llm.ContentTypeToolUse {
					prevToolUses[content.ID] = true
				}
			}
		} else if msg.Role == llm.MessageRoleUser {
			for _, content := range msg.Content {
				if content.Type == llm.ContentTypeToolResult {
					if prevToolUses != nil && !prevToolUses[content.ToolUseID] {
						t.Errorf("BUG: Found orphan tool_result at message index %d", i)
					}
				}
			}
			prevToolUses = nil
		}
	}
}
