package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/loop"
)

// TestChangeDirAffectsBash tests that change_dir updates the working directory
// and subsequent bash commands run in that directory.
func TestChangeDirAffectsBash(t *testing.T) {
	// Create a temp directory structure
	tmpDir := t.TempDir()
	subDir := filepath.Join(tmpDir, "subdir")
	if err := os.Mkdir(subDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create a marker file in subdir
	markerFile := filepath.Join(subDir, "marker.txt")
	if err := os.WriteFile(markerFile, []byte("found"), 0o644); err != nil {
		t.Fatal(err)
	}

	database, cleanup := setupTestDB(t)
	defer cleanup()

	predictableService := loop.NewPredictableService()
	llmManager := &testLLMManager{service: predictableService}
	logger := slog.Default()

	// Create server with working directory set to tmpDir
	toolSetConfig := claudetool.ToolSetConfig{
		WorkingDir: tmpDir,
	}
	server := NewServer(database, llmManager, toolSetConfig, logger, true, "", "predictable", "", nil)

	// Create conversation
	conversation, err := database.CreateConversation(context.Background(), nil, true, nil, nil)
	if err != nil {
		t.Fatalf("failed to create conversation: %v", err)
	}
	conversationID := conversation.ConversationID

	// Step 1: Send change_dir command to change to subdir
	changeDirReq := ChatRequest{
		Message: "change_dir: " + subDir,
		Model:   "predictable",
	}
	changeDirBody, _ := json.Marshal(changeDirReq)

	req := httptest.NewRequest("POST", "/api/conversation/"+conversationID+"/chat", strings.NewReader(string(changeDirBody)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	server.handleChatConversation(w, req, conversationID)
	if w.Code != http.StatusAccepted {
		t.Fatalf("expected status 202 for change_dir, got %d: %s", w.Code, w.Body.String())
	}

	// Wait for change_dir to complete - look for the tool result message
	waitForMessageContaining(t, database, conversationID, "Changed working directory", 5*time.Second)

	// Step 2: Now send pwd command - should show subdir
	pwdReq := ChatRequest{
		Message: "bash: pwd",
		Model:   "predictable",
	}
	pwdBody, _ := json.Marshal(pwdReq)

	req2 := httptest.NewRequest("POST", "/api/conversation/"+conversationID+"/chat", strings.NewReader(string(pwdBody)))
	req2.Header.Set("Content-Type", "application/json")
	w2 := httptest.NewRecorder()

	server.handleChatConversation(w2, req2, conversationID)
	if w2.Code != http.StatusAccepted {
		t.Fatalf("expected status 202 for bash pwd, got %d: %s", w2.Code, w2.Body.String())
	}

	// Wait for bash pwd to complete - the second tool result should contain the subdir
	// We need to wait for 2 tool results: one from change_dir and one from pwd
	waitForBashResult(t, database, conversationID, subDir, 5*time.Second)
}

// waitForBashResult waits for a bash tool result containing the expected text.
func waitForBashResult(t *testing.T, database *db.DB, conversationID, expectedText string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		messages, err := database.ListMessages(context.Background(), conversationID)
		if err != nil {
			t.Fatalf("failed to get messages: %v", err)
		}

		// Look for a tool result from bash tool that contains the expected text
		for _, msg := range messages {
			if msg.LlmData == nil {
				continue
			}
			// The tool result for bash should contain the pwd output
			// We distinguish it from the change_dir result by looking for the newline at the end
			// (pwd outputs the path with a newline, change_dir outputs "Changed working directory to: ...")
			// JSON encodes newline as \n so we check for that
			if strings.Contains(*msg.LlmData, expectedText+`\n`) {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Print debug info on failure
	messages, _ := database.ListMessages(context.Background(), conversationID)
	t.Log("Messages in conversation:")
	for i, msg := range messages {
		t.Logf("  Message %d: type=%s", i, msg.Type)
		if msg.LlmData != nil {
			t.Logf("    data: %s", truncate(*msg.LlmData, 300))
		}
	}
	t.Fatalf("did not find bash result containing %q within %v", expectedText, timeout)
}

// waitForMessageContaining waits for a message containing the specified text.
func waitForMessageContaining(t *testing.T, database *db.DB, conversationID, text string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		messages, err := database.ListMessages(context.Background(), conversationID)
		if err != nil {
			t.Fatalf("failed to get messages: %v", err)
		}
		for _, msg := range messages {
			if msg.LlmData != nil && strings.Contains(*msg.LlmData, text) {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("did not find message containing %q within %v", text, timeout)
}

// getConversationMessages retrieves all messages for a conversation.
func getConversationMessages(database *db.DB, conversationID string) ([]generated.Message, error) {
	return database.ListMessages(context.Background(), conversationID)
}

// truncate truncates a string to maxLen characters.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
