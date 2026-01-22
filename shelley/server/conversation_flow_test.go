package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/loop"
)

// TestMessageQueuedDuringThinking tests that messages sent while the LLM is
// processing (thinking/tool execution) are properly queued and eventually processed.
func TestMessageQueuedDuringThinking(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	predictableService := loop.NewPredictableService()
	llmManager := &testLLMManager{service: predictableService}
	logger := slog.Default()
	server := NewServer(database, llmManager, claudetool.ToolSetConfig{}, logger, true, "", "predictable", "", nil)

	// Create conversation
	conversation, err := database.CreateConversation(context.Background(), nil, true, nil, nil)
	if err != nil {
		t.Fatalf("failed to create conversation: %v", err)
	}
	conversationID := conversation.ConversationID

	// Send first message that triggers a slow response via "delay:" prefix
	chatReq := ChatRequest{
		Message: "delay: 2",
		Model:   "predictable",
	}
	chatBody, _ := json.Marshal(chatReq)

	req := httptest.NewRequest("POST", "/api/conversation/"+conversationID+"/chat", strings.NewReader(string(chatBody)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	server.handleChatConversation(w, req, conversationID)
	if w.Code != http.StatusAccepted {
		t.Fatalf("expected status 202 for first message, got %d: %s", w.Code, w.Body.String())
	}

	// Wait for the LLM to start processing (but still be in the delay)
	time.Sleep(200 * time.Millisecond)

	// Now send a SECOND message while the first is still processing
	// This is the bug: this message should be immediately recorded and visible,
	// not lost until the first message finishes processing
	secondReq := ChatRequest{
		Message: "echo: second message while thinking",
		Model:   "predictable",
	}
	secondBody, _ := json.Marshal(secondReq)

	req2 := httptest.NewRequest("POST", "/api/conversation/"+conversationID+"/chat", strings.NewReader(string(secondBody)))
	req2.Header.Set("Content-Type", "application/json")
	w2 := httptest.NewRecorder()

	server.handleChatConversation(w2, req2, conversationID)
	if w2.Code != http.StatusAccepted {
		t.Fatalf("expected status 202 for second message, got %d: %s", w2.Code, w2.Body.String())
	}

	// The second message should be recorded in the database IMMEDIATELY
	// (or at least very soon), not waiting for the first message to finish
	// Wait a short time for the message to be recorded
	time.Sleep(100 * time.Millisecond)

	var messages []generated.Message
	err = database.Queries(context.Background(), func(q *generated.Queries) error {
		var qerr error
		messages, qerr = q.ListMessages(context.Background(), conversationID)
		return qerr
	})
	if err != nil {
		t.Fatalf("failed to get messages: %v", err)
	}

	// Look for the second user message in the database
	foundSecondUserMessage := false
	for _, msg := range messages {
		if msg.Type != string(db.MessageTypeUser) {
			continue
		}
		if msg.LlmData == nil {
			continue
		}
		var llmMsg llm.Message
		if err := json.Unmarshal([]byte(*msg.LlmData), &llmMsg); err != nil {
			continue
		}
		for _, content := range llmMsg.Content {
			if content.Type == llm.ContentTypeText && strings.Contains(content.Text, "second message while thinking") {
				foundSecondUserMessage = true
				break
			}
		}
	}

	if !foundSecondUserMessage {
		t.Error("BUG: second user message sent during LLM processing was not immediately recorded to database")
		t.Logf("Found %d messages total:", len(messages))
		for i, msg := range messages {
			t.Logf("  Message %d: type=%s", i, msg.Type)
		}
	}

	// Wait for everything to complete
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		err = database.Queries(context.Background(), func(q *generated.Queries) error {
			var qerr error
			messages, qerr = q.ListMessages(context.Background(), conversationID)
			return qerr
		})
		if err != nil {
			t.Fatalf("failed to get messages: %v", err)
		}
		// Look for response to second message
		for _, msg := range messages {
			if msg.Type != string(db.MessageTypeAgent) || msg.LlmData == nil {
				continue
			}
			var llmMsg llm.Message
			if err := json.Unmarshal([]byte(*msg.LlmData), &llmMsg); err != nil {
				continue
			}
			for _, content := range llmMsg.Content {
				if content.Type == llm.ContentTypeText && strings.Contains(content.Text, "second message while thinking") {
					// Found the response
					return
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Error("timed out waiting for response to second message")
}

// TestContextPreservedAfterCancel tests that conversation context is properly
// preserved after cancellation and the conversation can be resumed correctly.
func TestContextPreservedAfterCancel(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	predictableService := loop.NewPredictableService()
	llmManager := &testLLMManager{service: predictableService}
	logger := slog.Default()
	server := NewServer(database, llmManager, claudetool.ToolSetConfig{}, logger, true, "", "predictable", "", nil)

	// Create conversation
	conversation, err := database.CreateConversation(context.Background(), nil, true, nil, nil)
	if err != nil {
		t.Fatalf("failed to create conversation: %v", err)
	}
	conversationID := conversation.ConversationID

	// Send first message and let it complete
	chatReq := ChatRequest{
		Message: "echo: initial context message",
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

	// Wait for first message to complete
	time.Sleep(300 * time.Millisecond)

	// Now start a slow operation and cancel it
	slowReq := ChatRequest{
		Message: "bash: sleep 5",
		Model:   "predictable",
	}
	slowBody, _ := json.Marshal(slowReq)

	req2 := httptest.NewRequest("POST", "/api/conversation/"+conversationID+"/chat", strings.NewReader(string(slowBody)))
	req2.Header.Set("Content-Type", "application/json")
	w2 := httptest.NewRecorder()

	server.handleChatConversation(w2, req2, conversationID)
	if w2.Code != http.StatusAccepted {
		t.Fatalf("expected status 202, got %d: %s", w2.Code, w2.Body.String())
	}

	// Wait for tool to start
	time.Sleep(200 * time.Millisecond)

	// Cancel the conversation
	cancelReq := httptest.NewRequest("POST", "/api/conversation/"+conversationID+"/cancel", nil)
	cancelW := httptest.NewRecorder()
	server.handleCancelConversation(cancelW, cancelReq, conversationID)

	if cancelW.Code != http.StatusOK {
		t.Fatalf("expected cancel status 200, got %d: %s", cancelW.Code, cancelW.Body.String())
	}

	// Wait for cancellation to complete
	time.Sleep(200 * time.Millisecond)

	// Clear the predictable service request history so we can inspect the next request
	predictableService.ClearRequests()

	// Resume the conversation
	resumeReq := ChatRequest{
		Message: "echo: after cancel",
		Model:   "predictable",
	}
	resumeBody, _ := json.Marshal(resumeReq)

	req3 := httptest.NewRequest("POST", "/api/conversation/"+conversationID+"/chat", strings.NewReader(string(resumeBody)))
	req3.Header.Set("Content-Type", "application/json")
	w3 := httptest.NewRecorder()

	server.handleChatConversation(w3, req3, conversationID)
	if w3.Code != http.StatusAccepted {
		t.Fatalf("expected status 202 for resume, got %d: %s", w3.Code, w3.Body.String())
	}

	// Wait for the request to be processed
	time.Sleep(300 * time.Millisecond)

	// Check that the LLM request included the conversation history
	lastReq := predictableService.GetLastRequest()
	if lastReq == nil {
		t.Fatal("BUG: no LLM request was made after resume")
	}

	// The request should include ALL previous messages:
	// 1. Initial context message (user)
	// 2. Response to initial context (assistant)
	// 3. bash: sleep 5 (user)
	// 4. Assistant response with tool use
	// 5. Cancelled tool result (user)
	// 6. [Operation cancelled] (assistant)
	// 7. echo: after cancel (user)
	//
	// If context is lost, we'll only have the last message (#7)

	if len(lastReq.Messages) < 3 {
		t.Errorf("BUG: context lost after cancellation! Expected at least 3 messages in LLM request, got %d", len(lastReq.Messages))
		t.Log("Messages in request:")
		for i, msg := range lastReq.Messages {
			t.Logf("  Message %d: role=%s, content_count=%d", i, msg.Role, len(msg.Content))
			for j, content := range msg.Content {
				if content.Type == llm.ContentTypeText {
					// Truncate long text
					text := content.Text
					if len(text) > 100 {
						text = text[:100] + "..."
					}
					t.Logf("    Content %d: type=%s, text=%q", j, content.Type, text)
				} else {
					t.Logf("    Content %d: type=%s", j, content.Type)
				}
			}
		}
	}

	// Check that "initial context message" appears somewhere in the history
	foundInitialContext := false
	for _, msg := range lastReq.Messages {
		for _, content := range msg.Content {
			if content.Type == llm.ContentTypeText && strings.Contains(content.Text, "initial context message") {
				foundInitialContext = true
				break
			}
		}
	}

	if !foundInitialContext {
		t.Error("BUG: initial context message was not preserved after cancellation")
	}
}
