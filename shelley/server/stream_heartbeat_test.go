package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/db"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/loop"
)

// TestStreamResumeWithLastSequenceID verifies that using last_sequence_id
// parameter skips sending messages and sends a heartbeat instead.
func TestStreamResumeWithLastSequenceID(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create a conversation with some messages
	conv, err := database.CreateConversation(ctx, nil, true, nil, nil)
	if err != nil {
		t.Fatalf("Failed to create conversation: %v", err)
	}

	// Add a user message
	userMsg := llm.Message{
		Role:    llm.MessageRoleUser,
		Content: []llm.Content{{Type: llm.ContentTypeText, Text: "Hello"}},
	}
	msg1, err := database.CreateMessage(ctx, db.CreateMessageParams{
		ConversationID: conv.ConversationID,
		Type:           db.MessageTypeUser,
		LLMData:        userMsg,
	})
	if err != nil {
		t.Fatalf("Failed to create user message: %v", err)
	}

	// Add an agent message
	agentMsg := llm.Message{
		Role:      llm.MessageRoleAssistant,
		Content:   []llm.Content{{Type: llm.ContentTypeText, Text: "Hi there!"}},
		EndOfTurn: true,
	}
	msg2, err := database.CreateMessage(ctx, db.CreateMessageParams{
		ConversationID: conv.ConversationID,
		Type:           db.MessageTypeAgent,
		LLMData:        agentMsg,
	})
	if err != nil {
		t.Fatalf("Failed to create agent message: %v", err)
	}

	// Create server
	predictableService := loop.NewPredictableService()
	llmManager := &testLLMManager{service: predictableService}
	toolSetConfig := claudetool.ToolSetConfig{EnableBrowser: false}
	server := NewServer(database, llmManager, toolSetConfig, nil, true, "", "predictable", "", nil)

	mux := http.NewServeMux()
	server.RegisterRoutes(mux)

	// Test 1: Fresh connection (no last_sequence_id) - should get all messages
	t.Run("fresh_connection", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		req := httptest.NewRequest("GET", "/api/conversation/"+conv.ConversationID+"/stream", nil).WithContext(ctx)
		req.Header.Set("Accept", "text/event-stream")

		w := newResponseRecorderWithClose()

		done := make(chan struct{})
		go func() {
			defer close(done)
			mux.ServeHTTP(w, req)
		}()

		time.Sleep(300 * time.Millisecond)
		w.Close()
		cancel()
		<-done

		body := w.Body.String()
		if !strings.HasPrefix(body, "data: ") {
			t.Fatalf("Expected SSE data, got: %s", body)
		}

		jsonData := strings.TrimPrefix(strings.Split(body, "\n")[0], "data: ")
		var response StreamResponse
		if err := json.Unmarshal([]byte(jsonData), &response); err != nil {
			t.Fatalf("Failed to parse response: %v", err)
		}

		if len(response.Messages) != 2 {
			t.Errorf("Expected 2 messages, got %d", len(response.Messages))
		}
		if response.Heartbeat {
			t.Error("Fresh connection should not be a heartbeat")
		}
	})

	// Test 2: Resume with last_sequence_id - should get heartbeat with no messages
	t.Run("resume_connection", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		// Use the sequence ID of the last message
		req := httptest.NewRequest("GET", "/api/conversation/"+conv.ConversationID+"/stream?last_sequence_id="+string(rune('0'+msg2.SequenceID)), nil).WithContext(ctx)
		req.Header.Set("Accept", "text/event-stream")

		w := newResponseRecorderWithClose()

		done := make(chan struct{})
		go func() {
			defer close(done)
			mux.ServeHTTP(w, req)
		}()

		time.Sleep(300 * time.Millisecond)
		w.Close()
		cancel()
		<-done

		body := w.Body.String()
		if !strings.HasPrefix(body, "data: ") {
			t.Fatalf("Expected SSE data, got: %s", body)
		}

		jsonData := strings.TrimPrefix(strings.Split(body, "\n")[0], "data: ")
		var response StreamResponse
		if err := json.Unmarshal([]byte(jsonData), &response); err != nil {
			t.Fatalf("Failed to parse response: %v", err)
		}

		if len(response.Messages) != 0 {
			t.Errorf("Expected 0 messages when resuming, got %d", len(response.Messages))
		}
		if !response.Heartbeat {
			t.Error("Resume connection should be a heartbeat")
		}
		if response.ConversationState == nil {
			t.Error("Expected ConversationState in heartbeat")
		}
	})

	// Suppress unused variable warnings
	_ = msg1
}
