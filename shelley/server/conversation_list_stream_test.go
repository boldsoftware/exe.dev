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
	"shelley.exe.dev/loop"
)

// TestConversationStreamReceivesListUpdateForNewConversation tests that when subscribed
// to one conversation's stream, we receive updates about new conversations.
func TestConversationStreamReceivesListUpdateForNewConversation(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	predictableService := loop.NewPredictableService()
	llmManager := &testLLMManager{service: predictableService}
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelWarn}))
	server := NewServer(database, llmManager, claudetool.ToolSetConfig{}, logger, true, "", "predictable", "", nil)

	// Create a conversation to subscribe to
	conversation, err := database.CreateConversation(context.Background(), nil, true, nil, nil)
	if err != nil {
		t.Fatalf("failed to create conversation: %v", err)
	}

	// Get or create conversation manager to ensure the conversation is active
	_, err = server.getOrCreateConversationManager(context.Background(), conversation.ConversationID)
	if err != nil {
		t.Fatalf("failed to get conversation manager: %v", err)
	}

	// Start the conversation stream
	sseCtx, sseCancel := context.WithCancel(context.Background())
	defer sseCancel()

	sseRecorder := newFlusherRecorder()
	sseReq := httptest.NewRequest("GET", "/api/conversation/"+conversation.ConversationID+"/stream", nil)
	sseReq = sseReq.WithContext(sseCtx)

	sseStarted := make(chan struct{})
	sseDone := make(chan struct{})
	go func() {
		close(sseStarted)
		server.handleStreamConversation(sseRecorder, sseReq, conversation.ConversationID)
		close(sseDone)
	}()

	<-sseStarted

	// Wait for the initial event
	select {
	case <-sseRecorder.flushed:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for initial SSE event")
	}

	// Create another conversation via the API
	chatReq := ChatRequest{
		Message: "hello",
		Model:   "predictable",
	}
	chatBody, _ := json.Marshal(chatReq)
	req := httptest.NewRequest("POST", "/api/conversations/new", strings.NewReader(string(chatBody)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	server.handleNewConversation(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected status 201, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		ConversationID string `json:"conversation_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	// Wait for the conversation list update to come through the existing stream
	deadline := time.Now().Add(2 * time.Second)
	var receivedUpdate bool
	for time.Now().Before(deadline) && !receivedUpdate {
		select {
		case <-sseRecorder.flushed:
			chunks := sseRecorder.getChunks()
			for _, chunk := range chunks {
				// Check for conversation_list_update with the new conversation ID
				if strings.Contains(chunk, "conversation_list_update") && strings.Contains(chunk, resp.ConversationID) {
					receivedUpdate = true
					break
				}
			}
		case <-time.After(100 * time.Millisecond):
		}
	}

	if !receivedUpdate {
		t.Error("did not receive conversation list update for new conversation")
		chunks := sseRecorder.getChunks()
		t.Logf("SSE chunks received: %v", chunks)
	}

	sseCancel()
	<-sseDone
}

// TestConversationStreamReceivesListUpdateForRename tests that when subscribed
// to one conversation's stream, we receive updates when another conversation is renamed.
func TestConversationStreamReceivesListUpdateForRename(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	predictableService := loop.NewPredictableService()
	llmManager := &testLLMManager{service: predictableService}
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelWarn}))
	server := NewServer(database, llmManager, claudetool.ToolSetConfig{}, logger, true, "", "predictable", "", nil)

	// Create two conversations
	conv1, err := database.CreateConversation(context.Background(), nil, true, nil, nil)
	if err != nil {
		t.Fatalf("failed to create conversation 1: %v", err)
	}
	conv2, err := database.CreateConversation(context.Background(), nil, true, nil, nil)
	if err != nil {
		t.Fatalf("failed to create conversation 2: %v", err)
	}

	// Get or create conversation manager for conv1 (the one we'll subscribe to)
	_, err = server.getOrCreateConversationManager(context.Background(), conv1.ConversationID)
	if err != nil {
		t.Fatalf("failed to get conversation manager: %v", err)
	}

	// Start the conversation stream for conv1
	sseCtx, sseCancel := context.WithCancel(context.Background())
	defer sseCancel()

	sseRecorder := newFlusherRecorder()
	sseReq := httptest.NewRequest("GET", "/api/conversation/"+conv1.ConversationID+"/stream", nil)
	sseReq = sseReq.WithContext(sseCtx)

	sseStarted := make(chan struct{})
	sseDone := make(chan struct{})
	go func() {
		close(sseStarted)
		server.handleStreamConversation(sseRecorder, sseReq, conv1.ConversationID)
		close(sseDone)
	}()

	<-sseStarted

	// Wait for the initial event
	select {
	case <-sseRecorder.flushed:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for initial SSE event")
	}

	// Rename conv2
	renameReq := RenameRequest{Slug: "test-slug-rename"}
	renameBody, _ := json.Marshal(renameReq)
	req := httptest.NewRequest("POST", "/api/conversation/"+conv2.ConversationID+"/rename", strings.NewReader(string(renameBody)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	server.handleRenameConversation(w, req, conv2.ConversationID)
	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	// Wait for the conversation list update with the new slug
	deadline := time.Now().Add(2 * time.Second)
	var receivedUpdate bool
	for time.Now().Before(deadline) && !receivedUpdate {
		select {
		case <-sseRecorder.flushed:
			chunks := sseRecorder.getChunks()
			for _, chunk := range chunks {
				if strings.Contains(chunk, "conversation_list_update") && strings.Contains(chunk, "test-slug-rename") {
					receivedUpdate = true
					break
				}
			}
		case <-time.After(100 * time.Millisecond):
		}
	}

	if !receivedUpdate {
		t.Error("did not receive conversation list update for slug change")
		chunks := sseRecorder.getChunks()
		t.Logf("SSE chunks received: %v", chunks)
	}

	sseCancel()
	<-sseDone
}

// TestConversationStreamReceivesListUpdateForDelete tests that when subscribed
// to one conversation's stream, we receive updates when another conversation is deleted.
func TestConversationStreamReceivesListUpdateForDelete(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	predictableService := loop.NewPredictableService()
	llmManager := &testLLMManager{service: predictableService}
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelWarn}))
	server := NewServer(database, llmManager, claudetool.ToolSetConfig{}, logger, true, "", "predictable", "", nil)

	// Create two conversations
	conv1, err := database.CreateConversation(context.Background(), nil, true, nil, nil)
	if err != nil {
		t.Fatalf("failed to create conversation 1: %v", err)
	}
	conv2, err := database.CreateConversation(context.Background(), nil, true, nil, nil)
	if err != nil {
		t.Fatalf("failed to create conversation 2: %v", err)
	}

	// Get or create conversation manager for conv1
	_, err = server.getOrCreateConversationManager(context.Background(), conv1.ConversationID)
	if err != nil {
		t.Fatalf("failed to get conversation manager: %v", err)
	}

	// Start the conversation stream for conv1
	sseCtx, sseCancel := context.WithCancel(context.Background())
	defer sseCancel()

	sseRecorder := newFlusherRecorder()
	sseReq := httptest.NewRequest("GET", "/api/conversation/"+conv1.ConversationID+"/stream", nil)
	sseReq = sseReq.WithContext(sseCtx)

	sseStarted := make(chan struct{})
	sseDone := make(chan struct{})
	go func() {
		close(sseStarted)
		server.handleStreamConversation(sseRecorder, sseReq, conv1.ConversationID)
		close(sseDone)
	}()

	<-sseStarted

	// Wait for the initial event
	select {
	case <-sseRecorder.flushed:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for initial SSE event")
	}

	// Delete conv2
	req := httptest.NewRequest("POST", "/api/conversation/"+conv2.ConversationID+"/delete", nil)
	w := httptest.NewRecorder()

	server.handleDeleteConversation(w, req, conv2.ConversationID)
	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	// Wait for the delete update
	deadline := time.Now().Add(2 * time.Second)
	var receivedUpdate bool
	for time.Now().Before(deadline) && !receivedUpdate {
		select {
		case <-sseRecorder.flushed:
			chunks := sseRecorder.getChunks()
			for _, chunk := range chunks {
				if strings.Contains(chunk, "conversation_list_update") &&
					strings.Contains(chunk, `"type":"delete"`) &&
					strings.Contains(chunk, conv2.ConversationID) {
					receivedUpdate = true
					break
				}
			}
		case <-time.After(100 * time.Millisecond):
		}
	}

	if !receivedUpdate {
		t.Error("did not receive conversation list delete update")
		chunks := sseRecorder.getChunks()
		t.Logf("SSE chunks received: %v", chunks)
	}

	sseCancel()
	<-sseDone
}

// TestConversationStreamReceivesListUpdateForArchive tests that when subscribed
// to one conversation's stream, we receive updates when another conversation is archived.
func TestConversationStreamReceivesListUpdateForArchive(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	predictableService := loop.NewPredictableService()
	llmManager := &testLLMManager{service: predictableService}
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelWarn}))
	server := NewServer(database, llmManager, claudetool.ToolSetConfig{}, logger, true, "", "predictable", "", nil)

	// Create two conversations
	conv1, err := database.CreateConversation(context.Background(), nil, true, nil, nil)
	if err != nil {
		t.Fatalf("failed to create conversation 1: %v", err)
	}
	conv2, err := database.CreateConversation(context.Background(), nil, true, nil, nil)
	if err != nil {
		t.Fatalf("failed to create conversation 2: %v", err)
	}

	// Get or create conversation manager for conv1
	_, err = server.getOrCreateConversationManager(context.Background(), conv1.ConversationID)
	if err != nil {
		t.Fatalf("failed to get conversation manager: %v", err)
	}

	// Start the conversation stream for conv1
	sseCtx, sseCancel := context.WithCancel(context.Background())
	defer sseCancel()

	sseRecorder := newFlusherRecorder()
	sseReq := httptest.NewRequest("GET", "/api/conversation/"+conv1.ConversationID+"/stream", nil)
	sseReq = sseReq.WithContext(sseCtx)

	sseStarted := make(chan struct{})
	sseDone := make(chan struct{})
	go func() {
		close(sseStarted)
		server.handleStreamConversation(sseRecorder, sseReq, conv1.ConversationID)
		close(sseDone)
	}()

	<-sseStarted

	// Wait for the initial event
	select {
	case <-sseRecorder.flushed:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for initial SSE event")
	}

	// Archive conv2
	req := httptest.NewRequest("POST", "/api/conversation/"+conv2.ConversationID+"/archive", nil)
	w := httptest.NewRecorder()

	server.handleArchiveConversation(w, req, conv2.ConversationID)
	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	// Wait for the archive update
	deadline := time.Now().Add(2 * time.Second)
	var receivedUpdate bool
	for time.Now().Before(deadline) && !receivedUpdate {
		select {
		case <-sseRecorder.flushed:
			chunks := sseRecorder.getChunks()
			for _, chunk := range chunks {
				if strings.Contains(chunk, "conversation_list_update") &&
					strings.Contains(chunk, conv2.ConversationID) &&
					strings.Contains(chunk, `"archived":true`) {
					receivedUpdate = true
					break
				}
			}
		case <-time.After(100 * time.Millisecond):
		}
	}

	if !receivedUpdate {
		t.Error("did not receive conversation list archive update")
		chunks := sseRecorder.getChunks()
		t.Logf("SSE chunks received: %v", chunks)
	}

	sseCancel()
	<-sseDone
}
