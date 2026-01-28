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
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/loop"
)

// TestCancelAfterToolCompletesCreatesDuplicateToolResult reproduces the bug where
// cancelling a conversation after a tool has already completed creates a duplicate
// tool_result for the same tool_use_id.
//
// The bug is in CancelConversation's search logic: it finds the first tool_use in
// the last assistant message and immediately breaks without checking if that tool
// already has a result. This causes it to create a cancelled tool_result even when
// the tool already completed successfully.
//
// This leads to the Anthropic API error:
// "each tool_use must have a single result. Found multiple `tool_result` blocks with id: ..."
func TestCancelAfterToolCompletesCreatesDuplicateToolResult(t *testing.T) {
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

	// Start a conversation with a fast tool call that completes quickly
	chatReq := ChatRequest{
		Message: "bash: echo hello",
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

	// Wait for the tool to complete - this is important!
	// The bash command "echo hello" should complete very quickly
	deadline := time.Now().Add(5 * time.Second)
	var toolResultFound bool
	for time.Now().Before(deadline) {
		var messages []generated.Message
		err := database.Queries(context.Background(), func(q *generated.Queries) error {
			var qerr error
			messages, qerr = q.ListMessages(context.Background(), conversationID)
			return qerr
		})
		if err != nil {
			t.Fatalf("failed to get messages: %v", err)
		}

		// Look for a tool_result message
		for _, msg := range messages {
			if msg.Type != string(db.MessageTypeUser) || msg.LlmData == nil {
				continue
			}
			var llmMsg llm.Message
			if err := json.Unmarshal([]byte(*msg.LlmData), &llmMsg); err != nil {
				continue
			}
			for _, content := range llmMsg.Content {
				if content.Type == llm.ContentTypeToolResult && !content.ToolError {
					// Found a successful tool result
					toolResultFound = true
					break
				}
			}
			if toolResultFound {
				break
			}
		}
		if toolResultFound {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if !toolResultFound {
		t.Fatal("tool result was not found - tool didn't complete")
	}

	// Now cancel the conversation AFTER the tool has completed
	// This should NOT create a new tool_result because the tool already finished
	cancelReq := httptest.NewRequest("POST", "/api/conversation/"+conversationID+"/cancel", nil)
	cancelW := httptest.NewRecorder()

	server.handleCancelConversation(cancelW, cancelReq, conversationID)
	if cancelW.Code != http.StatusOK {
		t.Fatalf("cancel: expected status 200, got %d: %s", cancelW.Code, cancelW.Body.String())
	}

	// Wait for agent to stop working (cancel to process)
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !server.IsAgentWorking(conversationID) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Check the messages to see if there are duplicate tool_results for the same tool_use_id
	var messages []generated.Message
	err = database.Queries(context.Background(), func(q *generated.Queries) error {
		var qerr error
		messages, qerr = q.ListMessages(context.Background(), conversationID)
		return qerr
	})
	if err != nil {
		t.Fatalf("failed to get messages after cancel: %v", err)
	}

	// Count tool_results by tool_use_id
	toolResultsByID := make(map[string]int)
	for _, msg := range messages {
		if msg.LlmData == nil {
			continue
		}
		var llmMsg llm.Message
		if err := json.Unmarshal([]byte(*msg.LlmData), &llmMsg); err != nil {
			continue
		}
		for _, content := range llmMsg.Content {
			if content.Type == llm.ContentTypeToolResult && content.ToolUseID != "" {
				toolResultsByID[content.ToolUseID]++
			}
		}
	}

	// Check for duplicates - this is the bug!
	for toolID, count := range toolResultsByID {
		if count > 1 {
			t.Errorf("BUG: found %d tool_results for tool_use_id %s (expected 1)", count, toolID)
		}
	}

	// Clear requests to get a clean slate for the next request
	predictableService.ClearRequests()

	// Now try to continue the conversation - this should trigger the API error
	// if duplicates exist
	resumeReq := ChatRequest{
		Message: "echo: test after cancel",
		Model:   "predictable",
	}
	resumeBody, _ := json.Marshal(resumeReq)

	resumeChatReq := httptest.NewRequest("POST", "/api/conversation/"+conversationID+"/chat", strings.NewReader(string(resumeBody)))
	resumeChatReq.Header.Set("Content-Type", "application/json")
	resumeW := httptest.NewRecorder()

	server.handleChatConversation(resumeW, resumeChatReq, conversationID)
	if resumeW.Code != http.StatusAccepted {
		t.Fatalf("resume: expected status 202, got %d: %s", resumeW.Code, resumeW.Body.String())
	}

	// Wait for agent to stop working
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !server.IsAgentWorking(conversationID) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Check the last request sent to the LLM for duplicate tool_results
	lastRequest := predictableService.GetLastRequest()
	if lastRequest == nil {
		t.Fatal("no request was sent to the LLM")
	}

	// Count tool_results in the request by tool_use_id
	requestToolResultsByID := make(map[string]int)
	for _, msg := range lastRequest.Messages {
		for _, content := range msg.Content {
			if content.Type == llm.ContentTypeToolResult && content.ToolUseID != "" {
				requestToolResultsByID[content.ToolUseID]++
			}
		}
	}

	// Check for duplicates in the request - this would cause the Anthropic API error
	for toolID, count := range requestToolResultsByID {
		if count > 1 {
			t.Errorf("BUG: LLM request contains %d tool_results for tool_use_id %s (expected 1). "+
				"This would cause Anthropic API error: 'each tool_use must have a single result'",
				count, toolID)
		}
	}
}
