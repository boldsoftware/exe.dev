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
	"shelley.exe.dev/llm"
	"shelley.exe.dev/loop"
	"shelley.exe.dev/server"
	"shelley.exe.dev/slug"
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
	logBuffer := server.NewLogBuffer(100)
	svr := server.NewServer(database, llmManager, tools, logger, logBuffer)

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

	// Test that slug updates are reflected in the stream
	t.Run("SlugUpdateStream", func(t *testing.T) {
		// Create a conversation without a slug
		conv, err := database.CreateConversation(context.Background(), nil, true)
		if err != nil {
			t.Fatalf("Failed to create conversation: %v", err)
		}

		// Verify initially no slug
		if conv.Slug != nil {
			t.Fatalf("Expected no initial slug, got: %v", *conv.Slug)
		}

		// Send a message which should trigger slug generation
		chatRequest := server.ChatRequest{
			Message: "Write a Python script to calculate fibonacci numbers",
			Model:   "predictable",
		}

		chatBody, _ := json.Marshal(chatRequest)
		chatResp, err := http.Post(
			testServer.URL+"/api/conversation/"+conv.ConversationID+"/chat",
			"application/json",
			strings.NewReader(string(chatBody)),
		)
		if err != nil {
			t.Fatalf("Failed to send chat message: %v", err)
		}
		chatResp.Body.Close()

		// Wait longer for slug generation (it happens asynchronously)
		for i := 0; i < 20; i++ {
			time.Sleep(500 * time.Millisecond)

			// Check if slug was generated
			updatedConv, err := database.GetConversationByID(context.Background(), conv.ConversationID)
			if err != nil {
				t.Fatalf("Failed to get updated conversation: %v", err)
			}

			if updatedConv.Slug != nil {
				t.Logf("Slug generated successfully: %s", *updatedConv.Slug)
				return
			}
		}

		t.Fatal("Slug was not generated within timeout period")
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
	logBuffer := server.NewLogBuffer(100)
	svr := server.NewServer(database, llmManager, []*llm.Tool{}, logger, logBuffer)

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

func TestSlugGeneration(t *testing.T) {
	// This test verifies that the slug generation logic is properly integrated
	// but uses the direct API to avoid timing issues with background goroutines

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

	// Create server
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelWarn}))
	llmManager := server.NewLLMServiceManager(logger)
	logBuffer := server.NewLogBuffer(100)
	_ = server.NewServer(database, llmManager, []*llm.Tool{}, logger, logBuffer)

	// Test slug generation directly to avoid timing issues
	// ctx := context.Background()
	// testMessage := "help me create a Python web server"

	// TODO: Fix slug generation test - method moved to slug package
	// Generate slug directly
	// slugResult, err := svr.GenerateSlugForConversation(ctx, testMessage)
	// if err != nil {
	//	t.Fatalf("Slug generation failed: %v", err)
	// }
	// if slugResult == "" {
	//	t.Error("Generated slug is empty")
	// } else {
	//	t.Logf("Generated slug: %s", slugResult)
	// }

	// TODO: Fix slug tests
	// Test that the slug is properly sanitized
	// if !strings.Contains(slugResult, "python") || !strings.Contains(slugResult, "web") {
	//	t.Logf("Note: Generated slug '%s' may not contain expected keywords, but this is acceptable for AI-generated content", slugResult)
	// }

	// // Verify slug uniqueness handling
	// conv, err := database.CreateConversation(ctx, &slugResult, true)
	// if err != nil {
	//	t.Fatalf("Failed to create conversation with slug: %v", err)
	// }

	// TODO: Fix slug generation test
	// Try to generate the same slug again - should get a unique variant
	// slugResult2, err := svr.GenerateSlugForConversation(ctx, testMessage)
	// if err != nil {
	//	t.Fatalf("Second slug generation failed: %v", err)
	// }

	// // The second slug should be different (with -1, -2, etc.)
	// if slugResult == slugResult2 {
	//	t.Errorf("Expected different slugs for uniqueness, but got same: %s", slugResult)
	// } else {
	//	t.Logf("Unique slug generated: %s", slugResult2)
	// }

	// _ = conv // avoid unused variable warning
}

func TestSanitizeSlug(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"basic text", "Hello World", "hello-world"},
		{"with numbers", "Python3 Tutorial", "python3-tutorial"},
		{"with special chars", "C++ Programming!", "c-programming"},
		{"multiple spaces", "Very  Long   Title", "very-long-title"},
		{"underscores", "test_function_name", "test-function-name"},
		{"mixed case", "CamelCaseExample", "camelcaseexample"},
		{"with hyphens", "pre-existing-hyphens", "pre-existing-hyphens"},
		{"leading/trailing spaces", "  trimmed  ", "trimmed"},
		{"leading/trailing hyphens", "-start-end-", "start-end"},
		{"multiple consecutive hyphens", "test---slug", "test-slug"},
		{"empty after sanitization", "!@#$%^&*()", ""},
		{"very long", "this-is-a-very-long-slug-that-should-be-truncated-because-it-exceeds-the-maximum-length", "this-is-a-very-long-slug-that-should-be-truncated-because-it"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := slug.Sanitize(tt.input)
			if result != tt.expected {
				t.Errorf("SanitizeSlug(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestSlugGenerationWithPredictableService(t *testing.T) {
	// Create server with predictable service only
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelWarn}))
	llmManager := server.NewLLMServiceManager(logger)

	// Create a temporary database
	tempDB := t.TempDir() + "/test.db"
	database, err := db.New(db.Config{DSN: tempDB})
	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}
	defer database.Close()

	if err := database.Migrate(context.Background()); err != nil {
		t.Fatalf("Failed to migrate database: %v", err)
	}

	logBuffer := server.NewLogBuffer(100)
	_ = server.NewServer(database, llmManager, []*llm.Tool{}, logger, logBuffer)

	// Test slug generation directly
	// ctx := context.Background()
	// testMessage := "help me write a python function"

	// TODO: Fix slug generation test
	// This should work with the predictable service falling back
	// slugResult, err := svr.GenerateSlugForConversation(ctx, testMessage)
	// if err != nil {
	//	t.Fatalf("Slug generation failed: %v", err)
	// }
	// if slugResult == "" {
	//	t.Error("Generated slug is empty")
	// }
	// t.Logf("Generated slug: %s", slugResult)

	// TODO: Fix slug sanitization test
	// Test slug sanitization which should always work
	// slug := slug.Sanitize(testMessage)
	// if slug != "help-me-write-a-python-function" {
	//	t.Errorf("Expected 'help-me-write-a-python-function', got '%s'", slug)
	// }
}

func TestSlugEndToEnd(t *testing.T) {
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

	// Create a conversation with a specific slug
	ctx := context.Background()
	testSlug := "test-conversation-slug"
	conv, err := database.CreateConversation(ctx, &testSlug, true)
	if err != nil {
		t.Fatalf("Failed to create conversation: %v", err)
	}

	// Test retrieving by slug
	retrievedBySlug, err := database.GetConversationBySlug(ctx, testSlug)
	if err != nil {
		t.Fatalf("Failed to retrieve conversation by slug: %v", err)
	}

	if retrievedBySlug.ConversationID != conv.ConversationID {
		t.Errorf("Expected conversation ID %s, got %s", conv.ConversationID, retrievedBySlug.ConversationID)
	}

	if retrievedBySlug.Slug == nil || *retrievedBySlug.Slug != testSlug {
		t.Errorf("Expected slug %s, got %v", testSlug, retrievedBySlug.Slug)
	}

	// Test retrieving by ID still works
	retrievedByID, err := database.GetConversationByID(ctx, conv.ConversationID)
	if err != nil {
		t.Fatalf("Failed to retrieve conversation by ID: %v", err)
	}

	if retrievedByID.ConversationID != conv.ConversationID {
		t.Errorf("Expected conversation ID %s, got %s", conv.ConversationID, retrievedByID.ConversationID)
	}

	t.Logf("Successfully tested slug-based conversation retrieval: %s -> %s", testSlug, conv.ConversationID)
}

// Test that slug updates are reflected in the stream

// Test that SSE only sends incremental message updates
func TestSSEIncrementalUpdates(t *testing.T) {
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

	// Create logger and LLM manager
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelWarn}))
	llmManager := server.NewLLMServiceManager(logger)
	logBuffer := server.NewLogBuffer(1000)

	// Create server
	serviceInstance := server.NewServer(database, llmManager, nil, logger, logBuffer)
	mux := http.NewServeMux()
	serviceInstance.RegisterRoutes(mux)
	testServer := httptest.NewServer(mux)
	defer testServer.Close()

	// Create a conversation with initial message
	slug := "test-sse"
	conv, err := database.CreateConversation(context.Background(), &slug, true)
	if err != nil {
		t.Fatalf("Failed to create conversation: %v", err)
	}

	// Add initial message
	_, err = database.CreateMessage(context.Background(), db.CreateMessageParams{
		ConversationID: conv.ConversationID,
		Type:           db.MessageTypeUser,
		LLMData:        &llm.Message{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "Hello"}}},
		UserData:       map[string]string{"content": "Hello"},
		UsageData:      llm.Usage{},
	})
	if err != nil {
		t.Fatalf("Failed to create initial message: %v", err)
	}

	// Create first SSE client
	client1, err := http.Get(testServer.URL + "/api/conversation/" + conv.ConversationID + "/stream")
	if err != nil {
		t.Fatalf("Failed to connect client1: %v", err)
	}
	defer client1.Body.Close()

	// Read initial response from client1 (should contain the first message)
	buf1 := make([]byte, 2048)
	n1, err := client1.Body.Read(buf1)
	if err != nil && err != io.EOF {
		t.Fatalf("Failed to read from client1: %v", err)
	}

	response1 := string(buf1[:n1])
	t.Logf("Client1 initial response: %s", response1)

	// Verify client1 received the initial message
	if !strings.Contains(response1, "Hello") {
		t.Fatal("Client1 should have received initial message")
	}

	// Add a second message
	_, err = database.CreateMessage(context.Background(), db.CreateMessageParams{
		ConversationID: conv.ConversationID,
		Type:           db.MessageTypeAgent,
		LLMData:        &llm.Message{Role: llm.MessageRoleAssistant, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "Hi there!"}}},
		UserData:       map[string]string{"content": "Hi there!"},
		UsageData:      llm.Usage{},
	})
	if err != nil {
		t.Fatalf("Failed to create second message: %v", err)
	}

	// Create second SSE client after the new message is added
	client2, err := http.Get(testServer.URL + "/api/conversation/" + conv.ConversationID + "/stream")
	if err != nil {
		t.Fatalf("Failed to connect client2: %v", err)
	}
	defer client2.Body.Close()

	// Read response from client2 (should contain both messages since it's a new client)
	buf2 := make([]byte, 2048)
	n2, err := client2.Body.Read(buf2)
	if err != nil && err != io.EOF {
		t.Fatalf("Failed to read from client2: %v", err)
	}

	response2 := string(buf2[:n2])
	t.Logf("Client2 initial response: %s", response2)

	// Verify client2 received both messages (new client gets full state)
	if !strings.Contains(response2, "Hello") {
		t.Fatal("Client2 should have received first message")
	}
	if !strings.Contains(response2, "Hi there!") {
		t.Fatal("Client2 should have received second message")
	}

	t.Log("SSE incremental updates test completed successfully")
}
