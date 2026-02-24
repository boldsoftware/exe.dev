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

// TestDistillContentSentToLLM verifies that after distillation completes,
// the distilled user message is actually included in the LLM request
// when the user sends a follow-up message in the distilled conversation.
func TestDistillContentSentToLLM(t *testing.T) {
	h := NewTestHarness(t)

	// Create a source conversation with some messages
	h.NewConversation("echo hello world", "")
	h.WaitResponse()
	sourceConvID := h.convID

	// Distill the source conversation
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
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var distillResp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &distillResp); err != nil {
		t.Fatalf("failed to parse distill response: %v", err)
	}
	newConvID := distillResp["conversation_id"].(string)

	// Wait for the distillation to produce a user message
	var distilledText string
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
							distilledText = content.Text
						}
					}
				}
			}
		}
		if distilledText != "" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if distilledText == "" {
		t.Fatal("timed out waiting for distilled user message")
	}
	t.Logf("Distilled text: %q", distilledText)

	// Clear LLM request history so we can see only the next request
	h.llm.ClearRequests()

	// Now send a follow-up message to the distilled conversation
	h.convID = newConvID
	h.responsesCount = 0
	h.Chat("echo followup message")
	h.WaitResponse()

	// Inspect the LLM request that was sent
	reqs := h.llm.GetRecentRequests()
	if len(reqs) == 0 {
		t.Fatal("no LLM requests recorded after sending follow-up message")
	}

	// The first request after the chat should contain the distilled message
	// as one of the earlier user messages in the conversation history
	firstReq := reqs[0]

	t.Logf("LLM request has %d messages", len(firstReq.Messages))
	for i, msg := range firstReq.Messages {
		for _, content := range msg.Content {
			if content.Type == llm.ContentTypeText {
				t.Logf("  Message[%d] role=%s text=%q", i, msg.Role, truncateForLog(content.Text, 100))
			}
		}
	}

	// Verify the distilled text appears in the messages sent to the LLM
	found := false
	for _, msg := range firstReq.Messages {
		for _, content := range msg.Content {
			if content.Type == llm.ContentTypeText && content.Text == distilledText {
				found = true
				break
			}
		}
		if found {
			break
		}
	}

	if !found {
		t.Fatalf("distilled text was NOT found in the LLM request messages!\n"+
			"Distilled text: %q\n"+
			"This means the distillation content is not being sent to the LLM.",
			distilledText)
	}

	t.Log("SUCCESS: distilled content IS being sent to the LLM")
}

func truncateForLog(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// TestDistillContentSentToLLM_WithEarlySSE verifies that if the SSE stream
// is opened BEFORE distillation completes (causing Hydrate to run early),
// the distilled user message is still included in the LLM request.
func TestDistillContentSentToLLM_WithEarlySSE(t *testing.T) {
	h := NewTestHarness(t)

	// Create a source conversation with some messages
	h.NewConversation("echo hello world", "")
	h.WaitResponse()
	sourceConvID := h.convID

	// Distill the source conversation
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
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var distillResp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &distillResp); err != nil {
		t.Fatalf("failed to parse distill response: %v", err)
	}
	newConvID := distillResp["conversation_id"].(string)

	// Simulate what the UI does: open the SSE stream immediately,
	// which triggers getOrCreateConversationManager -> Hydrate BEFORE
	// the distilled message is written.
	// This forces hydration with an empty history.
	manager, err := h.server.getOrCreateConversationManager(context.Background(), newConvID, "")
	if err != nil {
		t.Fatalf("failed to get/create conversation manager: %v", err)
	}
	_ = manager // just force it to exist and be hydrated

	// Now wait for the distillation to produce a user message
	var distilledText string
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
							distilledText = content.Text
						}
					}
				}
			}
		}
		if distilledText != "" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if distilledText == "" {
		t.Fatal("timed out waiting for distilled user message")
	}
	t.Logf("Distilled text: %q", distilledText)

	// Clear LLM request history
	h.llm.ClearRequests()

	// Now send a follow-up message to the distilled conversation
	h.convID = newConvID
	h.responsesCount = 0
	h.Chat("echo followup message")
	h.WaitResponse()

	// Wait a moment for any async slug generation to also complete
	time.Sleep(200 * time.Millisecond)

	// Inspect ALL LLM requests that were sent
	reqs := h.llm.GetRecentRequests()
	if len(reqs) == 0 {
		t.Fatal("no LLM requests recorded after sending follow-up message")
	}

	t.Logf("Total LLM requests: %d", len(reqs))
	for ri, r := range reqs {
		t.Logf("Request[%d] has %d messages:", ri, len(r.Messages))
		for i, msg := range r.Messages {
			for _, content := range msg.Content {
				if content.Type == llm.ContentTypeText {
					t.Logf("  Message[%d] role=%s text=%q", i, msg.Role, truncateForLog(content.Text, 120))
				}
			}
		}
	}

	// Verify the distilled text appears in at least one of the LLM requests
	found := false
	for _, r := range reqs {
		for _, msg := range r.Messages {
			for _, content := range msg.Content {
				if content.Type == llm.ContentTypeText && content.Text == distilledText {
					found = true
					break
				}
			}
			if found {
				break
			}
		}
		if found {
			break
		}
	}

	if !found {
		t.Fatalf("BUG CONFIRMED: distilled text was NOT found in ANY LLM request messages!\n"+
			"Distilled text: %q\n"+
			"When the SSE stream is opened before distillation completes, "+
			"the ConversationManager hydrates with empty history and never reloads.",
			distilledText)
	}

	t.Log("SUCCESS: distilled content IS being sent to the LLM even with early SSE")
}

// TestDistillStatusUpdateReachesSSESubscriber verifies that when an SSE subscriber
// has already received the "in_progress" system message, the subsequent "complete"
// status update is delivered via broadcast (not publish), so the subscriber sees it.
// This is a regression test for https://github.com/boldsoftware/shelley/issues/117
// where the spinner would spin forever because Publish skipped subscribers that
// already had the message's sequence ID.
func TestDistillStatusUpdateReachesSSESubscriber(t *testing.T) {
	h := NewTestHarness(t)

	// Create a source conversation with some messages
	h.NewConversation("echo hello world", "")
	h.WaitResponse()
	sourceConvID := h.convID

	// Distill the source conversation
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
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var distillResp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &distillResp); err != nil {
		t.Fatalf("failed to parse distill response: %v", err)
	}
	newConvID := distillResp["conversation_id"].(string)

	// Open an SSE stream to the new conversation (like the UI does).
	// This will receive the initial "in_progress" system message.
	sseRecorder := newFlusherRecorder()
	sseCtx, sseCancel := context.WithCancel(context.Background())
	defer sseCancel()
	sseReq := httptest.NewRequest("GET", "/api/conversations/"+newConvID+"/stream", nil)
	sseReq = sseReq.WithContext(sseCtx)
	go func() {
		h.server.handleStreamConversation(sseRecorder, sseReq, newConvID)
	}()

	// Wait for the initial SSE message (should contain "in_progress")
	select {
	case <-sseRecorder.flushed:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for initial SSE message")
	}

	// Now wait for the distillation to complete by watching the SSE stream
	// for a message containing "complete" status
	var sawComplete bool
	for i := 0; i < 200; i++ {
		body := sseRecorder.getString()
		if strings.Contains(body, `distill_status\":\"complete`) {
			sawComplete = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if !sawComplete {
		// Check what was actually in the SSE stream
		t.Fatalf("SSE subscriber never received the 'complete' status update.\n"+
			"This is the distill spinner bug: the subscriber already had the message's sequence_id,\n"+
			"so Publish skipped it. The fix is to use Broadcast for in-place message updates.\n"+
			"SSE body: %s", sseRecorder.getString())
	}
}
