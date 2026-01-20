package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
)

// TestWorkingDirectoryConfiguration tests that the working directory (cwd) setting
// is properly passed through from HTTP requests to tool execution.
func TestWorkingDirectoryConfiguration(t *testing.T) {
	h := NewTestHarness(t)
	defer h.Close()

	t.Run("cwd_tmp", func(t *testing.T) {
		h.NewConversation("bash: pwd", "/tmp")
		result := strings.TrimSpace(h.WaitToolResult())
		// Resolve symlinks for comparison (on macOS, /tmp -> /private/tmp)
		expected, _ := filepath.EvalSymlinks("/tmp")
		if result != expected {
			t.Errorf("expected %q, got: %s", expected, result)
		}
	})

	t.Run("cwd_root", func(t *testing.T) {
		h.NewConversation("bash: pwd", "/")
		result := strings.TrimSpace(h.WaitToolResult())
		if result != "/" {
			t.Errorf("expected '/', got: %s", result)
		}
	})
}

// TestListDirectory tests the list-directory API endpoint used by the directory picker.
func TestListDirectory(t *testing.T) {
	h := NewTestHarness(t)
	defer h.Close()

	t.Run("list_tmp", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/list-directory?path=/tmp", nil)
		w := httptest.NewRecorder()
		h.server.handleListDirectory(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
		}

		var resp ListDirectoryResponse
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("failed to parse response: %v", err)
		}

		if resp.Path != "/tmp" {
			t.Errorf("expected path '/tmp', got: %s", resp.Path)
		}

		if resp.Parent != "/" {
			t.Errorf("expected parent '/', got: %s", resp.Parent)
		}
	})

	t.Run("list_root", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/list-directory?path=/", nil)
		w := httptest.NewRecorder()
		h.server.handleListDirectory(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
		}

		var resp ListDirectoryResponse
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("failed to parse response: %v", err)
		}

		if resp.Path != "/" {
			t.Errorf("expected path '/', got: %s", resp.Path)
		}

		// Root should have no parent
		if resp.Parent != "" {
			t.Errorf("expected no parent, got: %s", resp.Parent)
		}

		// Root should have at least some directories (tmp, etc, home, etc.)
		if len(resp.Entries) == 0 {
			t.Error("expected at least some entries in root")
		}
	})

	t.Run("list_default_path", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/list-directory", nil)
		w := httptest.NewRecorder()
		h.server.handleListDirectory(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
		}

		var resp ListDirectoryResponse
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("failed to parse response: %v", err)
		}

		// Should default to home directory
		homeDir, _ := os.UserHomeDir()
		if homeDir != "" && resp.Path != homeDir {
			t.Errorf("expected path '%s', got: %s", homeDir, resp.Path)
		}
	})

	t.Run("list_nonexistent", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/list-directory?path=/nonexistent/path/123456", nil)
		w := httptest.NewRecorder()
		h.server.handleListDirectory(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
		}

		var resp map[string]interface{}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("failed to parse response: %v", err)
		}

		if resp["error"] == nil {
			t.Error("expected error field in response")
		}
	})

	t.Run("list_file_not_directory", func(t *testing.T) {
		// Create a temp file
		f, err := os.CreateTemp("", "test")
		if err != nil {
			t.Fatalf("failed to create temp file: %v", err)
		}
		defer os.Remove(f.Name())
		f.Close()

		req := httptest.NewRequest("GET", "/api/list-directory?path="+f.Name(), nil)
		w := httptest.NewRecorder()
		h.server.handleListDirectory(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
		}

		var resp map[string]interface{}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("failed to parse response: %v", err)
		}

		errMsg, ok := resp["error"].(string)
		if !ok || errMsg != "path is not a directory" {
			t.Errorf("expected error 'path is not a directory', got: %v", resp["error"])
		}
	})

	t.Run("only_directories_returned", func(t *testing.T) {
		// Create a temp directory with both files and directories
		tmpDir, err := os.MkdirTemp("", "listdir_test")
		if err != nil {
			t.Fatalf("failed to create temp dir: %v", err)
		}
		defer os.RemoveAll(tmpDir)

		// Create a subdirectory
		subDir := tmpDir + "/subdir"
		if err := os.Mkdir(subDir, 0o755); err != nil {
			t.Fatalf("failed to create subdir: %v", err)
		}

		// Create a file
		file := tmpDir + "/file.txt"
		if err := os.WriteFile(file, []byte("test"), 0o644); err != nil {
			t.Fatalf("failed to create file: %v", err)
		}

		req := httptest.NewRequest("GET", "/api/list-directory?path="+tmpDir, nil)
		w := httptest.NewRecorder()
		h.server.handleListDirectory(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
		}

		var resp ListDirectoryResponse
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("failed to parse response: %v", err)
		}

		// Should only include the directory, not the file
		if len(resp.Entries) != 1 {
			t.Errorf("expected 1 entry, got: %d", len(resp.Entries))
		}

		if len(resp.Entries) > 0 && resp.Entries[0].Name != "subdir" {
			t.Errorf("expected entry 'subdir', got: %s", resp.Entries[0].Name)
		}
	})

	t.Run("hidden_directories_excluded", func(t *testing.T) {
		// Create a temp directory with a hidden directory
		tmpDir, err := os.MkdirTemp("", "listdir_hidden_test")
		if err != nil {
			t.Fatalf("failed to create temp dir: %v", err)
		}
		defer os.RemoveAll(tmpDir)

		// Create a visible subdirectory
		visibleDir := tmpDir + "/visible"
		if err := os.Mkdir(visibleDir, 0o755); err != nil {
			t.Fatalf("failed to create visible dir: %v", err)
		}

		// Create a hidden subdirectory
		hiddenDir := tmpDir + "/.hidden"
		if err := os.Mkdir(hiddenDir, 0o755); err != nil {
			t.Fatalf("failed to create hidden dir: %v", err)
		}

		req := httptest.NewRequest("GET", "/api/list-directory?path="+tmpDir, nil)
		w := httptest.NewRecorder()
		h.server.handleListDirectory(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
		}

		var resp ListDirectoryResponse
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("failed to parse response: %v", err)
		}

		// Should only include the visible directory, not the hidden one
		if len(resp.Entries) != 1 {
			t.Errorf("expected 1 entry, got: %d", len(resp.Entries))
		}

		if len(resp.Entries) > 0 && resp.Entries[0].Name != "visible" {
			t.Errorf("expected entry 'visible', got: %s", resp.Entries[0].Name)
		}
	})
}

// TestConversationCwdReturnedInList tests that CWD is returned in the conversations list.
func TestConversationCwdReturnedInList(t *testing.T) {
	h := NewTestHarness(t)
	defer h.Close()

	// Create a conversation with a specific CWD
	h.NewConversation("bash: pwd", "/tmp")
	h.WaitToolResult() // Wait for the conversation to complete

	// Get the conversations list
	req := httptest.NewRequest("GET", "/api/conversations", nil)
	w := httptest.NewRecorder()
	h.server.handleConversations(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	var convs []map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &convs); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	if len(convs) == 0 {
		t.Fatal("expected at least one conversation")
	}

	// Find our conversation
	found := false
	for _, conv := range convs {
		if conv["conversation_id"] == h.ConversationID() {
			found = true
			cwd, ok := conv["cwd"].(string)
			if !ok {
				t.Errorf("expected cwd to be a string, got: %T", conv["cwd"])
			}
			if cwd != "/tmp" {
				t.Errorf("expected cwd '/tmp', got: %s", cwd)
			}
			break
		}
	}

	if !found {
		t.Error("conversation not found in list")
	}
}

// TestSystemPromptUsesCwdFromConversation verifies that when a conversation
// is created with a specific cwd, the system prompt is generated using that
// directory (not the server's working directory). This tests the fix for
// https://github.com/boldsoftware/shelley/issues/30
func TestSystemPromptUsesCwdFromConversation(t *testing.T) {
	// Create a temp directory with an AGENTS.md file
	tmpDir, err := os.MkdirTemp("", "shelley_cwd_test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create an AGENTS.md file with unique content we can search for
	agentsContent := "UNIQUE_MARKER_FOR_CWD_TEST_XYZ123: This is test guidance."
	agentsFile := filepath.Join(tmpDir, "AGENTS.md")
	if err := os.WriteFile(agentsFile, []byte(agentsContent), 0o644); err != nil {
		t.Fatalf("failed to write AGENTS.md: %v", err)
	}

	h := NewTestHarness(t)
	defer h.Close()

	// Create a conversation with the temp directory as cwd
	h.NewConversation("bash: echo hello", tmpDir)
	h.WaitToolResult()

	// Get the system prompt from the database
	var messages []generated.Message
	err = h.db.Queries(context.Background(), func(q *generated.Queries) error {
		var qerr error
		messages, qerr = q.ListMessages(context.Background(), h.ConversationID())
		return qerr
	})
	if err != nil {
		t.Fatalf("failed to get messages: %v", err)
	}

	// Find the system message
	var systemPrompt string
	for _, msg := range messages {
		if msg.Type == "system" && msg.LlmData != nil {
			var llmMsg llm.Message
			if err := json.Unmarshal([]byte(*msg.LlmData), &llmMsg); err == nil {
				for _, content := range llmMsg.Content {
					if content.Type == llm.ContentTypeText {
						systemPrompt = content.Text
						break
					}
				}
			}
			break
		}
	}

	if systemPrompt == "" {
		t.Fatal("no system prompt found in messages")
	}

	// Verify the system prompt contains our unique marker from AGENTS.md
	if !strings.Contains(systemPrompt, "UNIQUE_MARKER_FOR_CWD_TEST_XYZ123") {
		t.Errorf("system prompt should contain content from AGENTS.md in the cwd directory")
		// Log first 1000 chars to help debug
		if len(systemPrompt) > 1000 {
			t.Logf("system prompt (first 1000 chars): %s...", systemPrompt[:1000])
		} else {
			t.Logf("system prompt: %s", systemPrompt)
		}
	}

	// Verify the working directory in the prompt is our temp directory
	if !strings.Contains(systemPrompt, tmpDir) {
		t.Errorf("system prompt should reference the cwd directory: %s", tmpDir)
	}
}
