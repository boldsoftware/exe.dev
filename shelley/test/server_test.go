package test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
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
	"shelley.exe.dev/loop"
	"shelley.exe.dev/server"
	"sketch.dev/llm"
)

func TestServerEndToEnd(t *testing.T) {
	// Create temporary database
	tempDB := t.TempDir() + "/test.db"
	database, err := db.New(db.Config{DSN: tempDB})
	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}
	defer database.Close()

	// Run migrations
	if err := database.Migrate(context.Background()); err != nil {
		t.Fatalf("Failed to migrate database: %v", err)
	}

	// Create logger first
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	// Create LLM service manager with predictable service
	llmManager := server.NewLLMServiceManager(logger)
	predictableService := loop.NewPredictableServiceWithTestResponses()
	// For testing, we'll override the manager's service selection
	_ = predictableService // will need to mock this properly

	// Set up tools
	bashTool := &claudetool.BashTool{Pwd: t.TempDir()}
	patchTool := &claudetool.PatchTool{}
	tools := []*llm.Tool{
		claudetool.Think,
		bashTool.Tool(),
		patchTool.Tool(),
	}

	// Create server
	svr := server.NewServer(database, llmManager, tools, logger)

	// Set up HTTP server
	mux := http.NewServeMux()
	svr.RegisterRoutes(mux)
	testServer := httptest.NewServer(mux)
	defer testServer.Close()

	t.Run("CreateAndListConversations", func(t *testing.T) {
		// Create a conversation
		// Using database directly instead of service
		slug := "test-conversation"
		conv, err := database.CreateConversation(context.Background(), &slug, true)
		if err != nil {
			t.Fatalf("Failed to create conversation: %v", err)
		}

		// List conversations
		resp, err := http.Get(testServer.URL + "/api/conversations")
		if err != nil {
			t.Fatalf("Failed to get conversations: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("Expected status 200, got %d", resp.StatusCode)
		}

		var conversations []generated.Conversation
		if err := json.NewDecoder(resp.Body).Decode(&conversations); err != nil {
			t.Fatalf("Failed to decode response: %v", err)
		}

		if len(conversations) != 1 {
			t.Fatalf("Expected 1 conversation, got %d", len(conversations))
		}

		if conversations[0].ConversationID != conv.ConversationID {
			t.Fatalf("Conversation ID mismatch")
		}
	})

	t.Run("ChatEndToEnd", func(t *testing.T) {
		// Create a conversation
		// Using database directly instead of service
		slug := "chat-test"
		conv, err := database.CreateConversation(context.Background(), &slug, true)
		if err != nil {
			t.Fatalf("Failed to create conversation: %v", err)
		}

		// Send a chat message using predictable model
		chatReq := map[string]interface{}{"message": "Hello, can you help me?", "model": "predictable"}
		reqBody, _ := json.Marshal(chatReq)

		resp, err := http.Post(
			testServer.URL+"/api/conversation/"+conv.ConversationID+"/chat",
			"application/json",
			bytes.NewReader(reqBody),
		)
		if err != nil {
			t.Fatalf("Failed to send chat message: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("Expected status 202, got %d", resp.StatusCode)
		}

		// Wait a bit for processing
		time.Sleep(500 * time.Millisecond)

		// Check messages
		msgResp, err := http.Get(testServer.URL + "/api/conversation/" + conv.ConversationID)
		if err != nil {
			t.Fatalf("Failed to get conversation: %v", err)
		}
		defer msgResp.Body.Close()

		if msgResp.StatusCode != http.StatusOK {
			t.Fatalf("Expected status 200, got %d", msgResp.StatusCode)
		}

		var messages []generated.Message
		if err := json.NewDecoder(msgResp.Body).Decode(&messages); err != nil {
			t.Fatalf("Failed to decode messages: %v", err)
		}

		// Should have at least the user message
		if len(messages) == 0 {
			t.Fatal("Expected at least 1 message")
		}

		// First message should be from user
		if messages[0].Type != "user" {
			t.Fatalf("Expected first message to be user, got %s", messages[0].Type)
		}
	})

	t.Run("StreamEndpoint", func(t *testing.T) {
		// Create a conversation with some messages
		// Using database directly instead of service
		// Using database directly instead of service
		slug := "stream-test"
		conv, err := database.CreateConversation(context.Background(), &slug, true)
		if err != nil {
			t.Fatalf("Failed to create conversation: %v", err)
		}

		// Add a test message
		testMsg := llm.Message{
			Role: llm.MessageRoleUser,
			Content: []llm.Content{
				{Type: llm.ContentTypeText, Text: "Test message"},
			},
		}
		_, err = database.CreateMessage(context.Background(), db.CreateMessageParams{
			ConversationID: conv.ConversationID,
			Type:           db.MessageTypeUser,
			LLMData:        testMsg,
		})
		if err != nil {
			t.Fatalf("Failed to create message: %v", err)
		}

		// Test stream endpoint
		resp, err := http.Get(testServer.URL + "/api/conversation/" + conv.ConversationID + "/stream")
		if err != nil {
			t.Fatalf("Failed to get stream: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("Expected status 200, got %d", resp.StatusCode)
		}

		// Check headers
		if resp.Header.Get("Content-Type") != "text/event-stream" {
			t.Fatal("Expected text/event-stream content type")
		}

		// Read first event (should be current messages)
		buf := make([]byte, 1024)
		n, err := resp.Body.Read(buf)
		if err != nil && err != io.EOF {
			t.Fatalf("Failed to read stream: %v", err)
		}

		data := string(buf[:n])
		if !strings.Contains(data, "data: ") {
			t.Fatal("Expected SSE data format")
		}
	})

	t.Run("ErrorHandling", func(t *testing.T) {
		// Test non-existent conversation
		resp, err := http.Get(testServer.URL + "/api/conversation/nonexistent")
		if err != nil {
			t.Fatalf("Failed to make request: %v", err)
		}
		defer resp.Body.Close()

		// Should handle gracefully (might be empty list or error depending on implementation)
		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNotFound {
			t.Fatalf("Unexpected status code: %d", resp.StatusCode)
		}

		// Test invalid chat request
		invalidReq := map[string]string{"not_message": "test"}
		reqBody, _ := json.Marshal(invalidReq)
		chatResp, err := http.Post(
			testServer.URL+"/api/conversation/test/chat",
			"application/json",
			bytes.NewReader(reqBody),
		)
		if err != nil {
			t.Fatalf("Failed to send invalid chat: %v", err)
		}
		defer chatResp.Body.Close()

		if chatResp.StatusCode != http.StatusBadRequest {
			t.Fatalf("Expected status 400 for invalid request, got %d", chatResp.StatusCode)
		}
	})
}

func TestPredictableServiceWithTools(t *testing.T) {
	// Test that the predictable service correctly handles tool calls
	service := loop.NewPredictableServiceWithTestResponses()

	// First call should return greeting
	resp1, err := service.Do(context.Background(), &llm.Request{
		Messages: []llm.Message{
			{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "Hello"}}},
		},
	})
	if err != nil {
		t.Fatalf("First call failed: %v", err)
	}

	if !strings.Contains(resp1.Content[0].Text, "Shelley") {
		t.Fatal("Expected greeting to mention Shelley")
	}

	// Second call should return tool use
	resp2, err := service.Do(context.Background(), &llm.Request{
		Messages: []llm.Message{
			{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "Create an example"}}},
		},
	})
	if err != nil {
		t.Fatalf("Second call failed: %v", err)
	}

	if resp2.StopReason != llm.StopReasonToolUse {
		t.Fatal("Expected tool use stop reason")
	}

	if len(resp2.Content) < 2 {
		t.Fatal("Expected both text and tool use content")
	}

	// Find tool use content
	var toolUse *llm.Content
	for i := range resp2.Content {
		if resp2.Content[i].Type == llm.ContentTypeToolUse {
			toolUse = &resp2.Content[i]
			break
		}
	}

	if toolUse == nil {
		t.Fatal("Expected tool use content")
	}

	if toolUse.ToolName != "think" {
		t.Fatalf("Expected think tool, got %s", toolUse.ToolName)
	}
}

func TestConversationCleanup(t *testing.T) {
	// Create temporary database
	tempDB := t.TempDir() + "/cleanup_test.db"
	database, err := db.New(db.Config{DSN: tempDB})
	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}
	defer database.Close()

	// Run migrations
	if err := database.Migrate(context.Background()); err != nil {
		t.Fatalf("Failed to migrate database: %v", err)
	}

	// Create server with predictable service
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))
	llmManager := server.NewLLMServiceManager(logger)
	svr := server.NewServer(database, llmManager, []*llm.Tool{}, logger)

	// Create a conversation
	// Using database directly instead of service
	conv, err := database.CreateConversation(context.Background(), nil, true)
	if err != nil {
		t.Fatalf("Failed to create conversation: %v", err)
	}

	// Test cleanup indirectly by calling cleanup
	svr.Cleanup()

	// Test passes if no panic occurs
	t.Log("Cleanup completed successfully for conversation:", conv.ConversationID)
}
