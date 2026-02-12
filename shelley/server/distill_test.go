package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
)

func TestDistillConversation(t *testing.T) {
	h := NewTestHarness(t)
	defer h.Close()

	// Create a conversation with some messages
	h.NewConversation("echo hello world", "")
	h.WaitResponse()
	sourceConvID := h.convID

	// Now call the distill endpoint
	reqBody := ContinueConversationRequest{
		SourceConversationID: sourceConvID,
		Model:                "predictable",
	}
	body, _ := json.Marshal(reqBody)
	req := httptest.NewRequest("POST", "/api/conversations/distill", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.server.handleDistillConversation(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected status 201, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	newConvID, ok := resp["conversation_id"].(string)
	if !ok || newConvID == "" {
		t.Fatal("expected conversation_id in response")
	}

	// The new conversation should exist
	newConv, err := h.db.GetConversationByID(context.Background(), newConvID)
	if err != nil {
		t.Fatalf("failed to get new conversation: %v", err)
	}
	if newConv.Model == nil || *newConv.Model != "predictable" {
		t.Fatalf("expected model 'predictable', got %v", newConv.Model)
	}

	// There should be a system message initially (the status message)
	var hasSystemMsg bool
	for i := 0; i < 50; i++ {
		msgs, err := h.db.ListMessages(context.Background(), newConvID)
		if err != nil {
			t.Fatalf("failed to list messages: %v", err)
		}
		for _, msg := range msgs {
			if msg.Type == string(db.MessageTypeSystem) {
				hasSystemMsg = true
			}
		}
		if hasSystemMsg {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !hasSystemMsg {
		t.Fatal("expected a system status message")
	}

	// Wait for the distillation to complete (a user message should appear)
	var userMsg *string
	for i := 0; i < 100; i++ {
		msgs, err := h.db.ListMessages(context.Background(), newConvID)
		if err != nil {
			t.Fatalf("failed to list messages: %v", err)
		}
		for _, msg := range msgs {
			if msg.Type == string(db.MessageTypeUser) && msg.LlmData != nil {
				var llmMsg llm.Message
				if err := json.Unmarshal([]byte(*msg.LlmData), &llmMsg); err == nil {
					for _, content := range llmMsg.Content {
						if content.Type == llm.ContentTypeText && content.Text != "" {
							userMsg = &content.Text
						}
					}
				}
			}
		}
		if userMsg != nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if userMsg == nil {
		t.Fatal("expected a user message with distilled content")
	}

	// The distilled message should contain some text (from the predictable service)
	if len(*userMsg) == 0 {
		t.Fatal("distilled message was empty")
	}

	// The status message should be updated to "complete"
	msgs, err := h.db.ListMessages(context.Background(), newConvID)
	if err != nil {
		t.Fatalf("failed to list messages: %v", err)
	}
	var statusComplete bool
	for _, msg := range msgs {
		if msg.Type == string(db.MessageTypeSystem) && msg.UserData != nil {
			var userData map[string]string
			if err := json.Unmarshal([]byte(*msg.UserData), &userData); err == nil {
				if userData["distill_status"] == "complete" {
					statusComplete = true
				}
			}
		}
	}
	if !statusComplete {
		t.Fatal("expected distill status to be 'complete'")
	}
}

func TestDistillConversationMissingSource(t *testing.T) {
	h := NewTestHarness(t)
	defer h.Close()

	reqBody := ContinueConversationRequest{
		SourceConversationID: "nonexistent-id",
		Model:                "predictable",
	}
	body, _ := json.Marshal(reqBody)
	req := httptest.NewRequest("POST", "/api/conversations/distill", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.server.handleDistillConversation(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected status 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestDistillConversationEmptySource(t *testing.T) {
	h := NewTestHarness(t)
	defer h.Close()

	reqBody := ContinueConversationRequest{
		SourceConversationID: "",
	}
	body, _ := json.Marshal(reqBody)
	req := httptest.NewRequest("POST", "/api/conversations/distill", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.server.handleDistillConversation(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestBuildDistillTranscript(t *testing.T) {
	// Nil messages: only slug header.
	transcript := buildDistillTranscript("test-convo", nil)
	if !strings.Contains(transcript, "test-convo") {
		t.Fatal("expected slug in transcript")
	}

	makeMsg := func(typ string, llmMsg llm.Message) generated.Message {
		data, _ := json.Marshal(llmMsg)
		s := string(data)
		return generated.Message{Type: typ, LlmData: &s}
	}

	// User text message
	msgs := []generated.Message{
		makeMsg(string(db.MessageTypeUser), llm.Message{
			Role:    llm.MessageRoleUser,
			Content: []llm.Content{{Type: llm.ContentTypeText, Text: "hello world"}},
		}),
	}
	transcript = buildDistillTranscript("slug", msgs)
	if !strings.Contains(transcript, "User: hello world") {
		t.Fatalf("expected user text, got: %s", transcript)
	}

	// Agent text gets truncated at 2000 bytes
	longText := strings.Repeat("x", 3000)
	msgs = []generated.Message{
		makeMsg(string(db.MessageTypeAgent), llm.Message{
			Role:    llm.MessageRoleAssistant,
			Content: []llm.Content{{Type: llm.ContentTypeText, Text: longText}},
		}),
	}
	transcript = buildDistillTranscript("slug", msgs)
	if strings.Contains(transcript, longText) {
		t.Fatal("expected long text to be truncated")
	}
	if !strings.Contains(transcript, "...") {
		t.Fatal("expected truncation indicator")
	}

	// Tool use with long input
	msgs = []generated.Message{
		makeMsg(string(db.MessageTypeAgent), llm.Message{
			Role: llm.MessageRoleAssistant,
			Content: []llm.Content{{
				Type:      llm.ContentTypeToolUse,
				ToolName:  "bash",
				ToolInput: json.RawMessage(`"` + strings.Repeat("a", 600) + `"`),
			}},
		}),
	}
	transcript = buildDistillTranscript("slug", msgs)
	if !strings.Contains(transcript, "[Tool: bash]") {
		t.Fatalf("expected tool use, got: %s", transcript)
	}

	// Tool result with error flag
	msgs = []generated.Message{
		makeMsg(string(db.MessageTypeUser), llm.Message{
			Role: llm.MessageRoleUser,
			Content: []llm.Content{{
				Type:       llm.ContentTypeToolResult,
				ToolError:  true,
				ToolResult: []llm.Content{{Type: llm.ContentTypeText, Text: "command not found"}},
			}},
		}),
	}
	transcript = buildDistillTranscript("slug", msgs)
	if !strings.Contains(transcript, "(error)") {
		t.Fatalf("expected error flag, got: %s", transcript)
	}
	if !strings.Contains(transcript, "command not found") {
		t.Fatalf("expected error text, got: %s", transcript)
	}

	// System messages are skipped
	msgs = []generated.Message{
		{Type: string(db.MessageTypeSystem)},
		makeMsg(string(db.MessageTypeUser), llm.Message{
			Role:    llm.MessageRoleUser,
			Content: []llm.Content{{Type: llm.ContentTypeText, Text: "visible"}},
		}),
	}
	transcript = buildDistillTranscript("slug", msgs)
	if strings.Contains(transcript, "System") {
		t.Fatal("system messages should be skipped")
	}
	if !strings.Contains(transcript, "visible") {
		t.Fatal("user message should be present")
	}

	// Nil LlmData is skipped
	msgs = []generated.Message{
		{Type: string(db.MessageTypeUser), LlmData: nil},
	}
	transcript = buildDistillTranscript("slug", msgs)
	// Should just have the slug header with no crash
	if !strings.Contains(transcript, "slug") {
		t.Fatal("expected slug")
	}
}

func TestTruncateUTF8(t *testing.T) {
	// No truncation needed
	result := truncateUTF8("hello", 10)
	if result != "hello" {
		t.Fatalf("expected 'hello', got %q", result)
	}

	result = truncateUTF8("hello world", 5)
	if result != "hello..." {
		t.Fatalf("expected 'hello...', got %q", result)
	}

	// Multi-byte: don't split a rune. "é" is 2 bytes (0xC3 0xA9).
	// "aé" = 3 bytes. Truncating at 2 should not split the é.
	result = truncateUTF8("aé", 2)
	if result != "a..." {
		t.Fatalf("expected 'a...', got %q", result)
	}

	// Exactly fitting multi-byte
	result = truncateUTF8("aé", 3)
	if result != "aé" {
		t.Fatalf("expected 'aé', got %q", result)
	}

	// Empty string
	result = truncateUTF8("", 5)
	if result != "" {
		t.Fatalf("expected empty, got %q", result)
	}

	// 4-byte char (emoji: 🎉)
	result = truncateUTF8("a🎉b", 2)
	if result != "a..." {
		t.Fatalf("expected 'a...', got %q", result)
	}
}
