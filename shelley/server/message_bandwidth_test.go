package server

import (
	"bufio"
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
	"shelley.exe.dev/loop"
)

// TestMessageSentOnlyOnce verifies that each message is sent to SSE subscribers
// only once, not with every update.
func TestMessageSentOnlyOnce(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	predictableService := loop.NewPredictableService()
	llmManager := &testLLMManager{service: predictableService}
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelWarn}))

	server := NewServer(database, llmManager, claudetool.ToolSetConfig{}, logger, true, "", "predictable", "", nil)

	// Create conversation
	conversation, err := database.CreateConversation(context.Background(), nil, true, nil, nil)
	if err != nil {
		t.Fatalf("failed to create conversation: %v", err)
	}
	conversationID := conversation.ConversationID

	// Set up real HTTP server
	mux := http.NewServeMux()
	server.RegisterRoutes(mux)
	httpServer := httptest.NewServer(mux)
	defer httpServer.Close()

	// Connect to SSE stream
	sseResp, err := http.Get(httpServer.URL + "/api/conversation/" + conversationID + "/stream")
	if err != nil {
		t.Fatalf("failed to connect to SSE stream: %v", err)
	}
	defer sseResp.Body.Close()

	// Start reading SSE events in background
	type sseEvent struct {
		data      StreamResponse
		msgCount  int
		totalSize int
	}
	sseEvents := make(chan sseEvent, 100)

	go func() {
		scanner := bufio.NewScanner(sseResp.Body)
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			jsonStr := strings.TrimPrefix(line, "data: ")
			var streamResp StreamResponse
			if err := json.Unmarshal([]byte(jsonStr), &streamResp); err != nil {
				continue
			}
			sseEvents <- sseEvent{
				data:      streamResp,
				msgCount:  len(streamResp.Messages),
				totalSize: len(jsonStr),
			}
		}
	}()

	// Wait for initial SSE event (empty)
	select {
	case ev := <-sseEvents:
		t.Logf("Initial SSE event: %d messages, %d bytes", ev.msgCount, ev.totalSize)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for initial SSE event")
	}

	// Send first user message
	chatReq := ChatRequest{
		Message: "hello",
		Model:   "predictable",
	}
	chatBody, _ := json.Marshal(chatReq)

	resp, err := http.Post(
		httpServer.URL+"/api/conversation/"+conversationID+"/chat",
		"application/json",
		strings.NewReader(string(chatBody)),
	)
	if err != nil {
		t.Fatalf("failed to send chat message: %v", err)
	}
	resp.Body.Close()

	// Collect SSE events for a short time to see the message progression
	var receivedEvents []sseEvent
	deadline := time.Now().Add(3 * time.Second)

	for time.Now().Before(deadline) {
		select {
		case ev := <-sseEvents:
			receivedEvents = append(receivedEvents, ev)
			t.Logf("SSE event %d: %d messages, %d bytes", len(receivedEvents), ev.msgCount, ev.totalSize)

			// Check if we have end_of_turn
			if len(ev.data.Messages) > 0 {
				lastMsg := ev.data.Messages[len(ev.data.Messages)-1]
				if lastMsg.EndOfTurn != nil && *lastMsg.EndOfTurn {
					t.Log("Got end_of_turn, stopping collection")
					goto done
				}
			}
		case <-time.After(100 * time.Millisecond):
			// Keep waiting
		}
	}

done:
	if len(receivedEvents) == 0 {
		t.Fatal("received no SSE events after sending message")
	}

	// Analyze: count how many times each message was sent
	messagesSent := make(map[int64]int) // sequence_id -> count
	totalBytes := 0

	for _, ev := range receivedEvents {
		totalBytes += ev.totalSize
		for _, msg := range ev.data.Messages {
			messagesSent[msg.SequenceID]++
		}
	}

	t.Logf("Total bytes sent across all SSE events: %d", totalBytes)
	t.Logf("Message send counts:")
	for seqID, count := range messagesSent {
		t.Logf("  Sequence %d: sent %d times", seqID, count)
		if count > 1 {
			t.Errorf("BUG: Message with sequence_id=%d was sent %d times (expected 1)", seqID, count)
		}
	}
}

// TestContextWindowSizeInSSE verifies that context_window_size is correctly
// included only when agent messages with usage data are sent.
func TestContextWindowSizeInSSE(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	predictableService := loop.NewPredictableService()
	llmManager := &testLLMManager{service: predictableService}
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelWarn}))

	server := NewServer(database, llmManager, claudetool.ToolSetConfig{}, logger, true, "", "predictable", "", nil)

	// Create conversation
	conversation, err := database.CreateConversation(context.Background(), nil, true, nil, nil)
	if err != nil {
		t.Fatalf("failed to create conversation: %v", err)
	}
	conversationID := conversation.ConversationID

	// Set up real HTTP server
	mux := http.NewServeMux()
	server.RegisterRoutes(mux)
	httpServer := httptest.NewServer(mux)
	defer httpServer.Close()

	// Connect to SSE stream
	sseResp, err := http.Get(httpServer.URL + "/api/conversation/" + conversationID + "/stream")
	if err != nil {
		t.Fatalf("failed to connect to SSE stream: %v", err)
	}
	defer sseResp.Body.Close()

	// Start reading SSE events in background
	type sseEvent struct {
		data              StreamResponse
		contextWindowSize uint64
		hasContextWindow  bool
	}
	sseEvents := make(chan sseEvent, 100)

	go func() {
		scanner := bufio.NewScanner(sseResp.Body)
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			jsonStr := strings.TrimPrefix(line, "data: ")
			var streamResp StreamResponse
			if err := json.Unmarshal([]byte(jsonStr), &streamResp); err != nil {
				continue
			}
			// Check if context_window_size was present in the JSON
			var raw map[string]interface{}
			json.Unmarshal([]byte(jsonStr), &raw)
			_, hasCtx := raw["context_window_size"]

			sseEvents <- sseEvent{
				data:              streamResp,
				contextWindowSize: streamResp.ContextWindowSize,
				hasContextWindow:  hasCtx,
			}
		}
	}()

	// Wait for initial SSE event (empty)
	select {
	case ev := <-sseEvents:
		t.Logf("Initial: context_window_size present=%v value=%d", ev.hasContextWindow, ev.contextWindowSize)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for initial SSE event")
	}

	// Send user message
	chatReq := ChatRequest{
		Message: "hello",
		Model:   "predictable",
	}
	chatBody, _ := json.Marshal(chatReq)

	resp, err := http.Post(
		httpServer.URL+"/api/conversation/"+conversationID+"/chat",
		"application/json",
		strings.NewReader(string(chatBody)),
	)
	if err != nil {
		t.Fatalf("failed to send chat message: %v", err)
	}
	resp.Body.Close()

	// Collect SSE events
	var receivedEvents []sseEvent
	deadline := time.Now().Add(3 * time.Second)

	for time.Now().Before(deadline) {
		select {
		case ev := <-sseEvents:
			receivedEvents = append(receivedEvents, ev)
			msgType := "unknown"
			if len(ev.data.Messages) > 0 {
				msgType = ev.data.Messages[0].Type
			}
			t.Logf("Event %d: type=%s context_window_size present=%v value=%d",
				len(receivedEvents), msgType, ev.hasContextWindow, ev.contextWindowSize)

			// Check if we have end_of_turn
			if len(ev.data.Messages) > 0 {
				lastMsg := ev.data.Messages[len(ev.data.Messages)-1]
				if lastMsg.EndOfTurn != nil && *lastMsg.EndOfTurn {
					goto done
				}
			}
		case <-time.After(100 * time.Millisecond):
		}
	}

done:
	// Verify: user messages should NOT have context_window_size (omitted via omitempty)
	// Agent messages with usage data SHOULD have context_window_size
	for i, ev := range receivedEvents {
		if len(ev.data.Messages) == 0 {
			continue
		}
		msg := ev.data.Messages[0]
		if msg.Type == "user" {
			// User messages have no usage data, context_window_size should be omitted (0)
			if ev.hasContextWindow && ev.contextWindowSize != 0 {
				t.Errorf("Event %d: user message should not have context_window_size, got %d", i+1, ev.contextWindowSize)
			}
		} else if msg.Type == "agent" && msg.UsageData != nil {
			// Agent messages with usage data should have context_window_size
			if !ev.hasContextWindow {
				t.Errorf("Event %d: agent message with usage data should have context_window_size", i+1)
			}
			if ev.contextWindowSize == 0 {
				t.Errorf("Event %d: agent message context_window_size should not be 0", i+1)
			}
		}
	}
}
