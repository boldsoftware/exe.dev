package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
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

	t.Run("hidden_directories_sorted_last", func(t *testing.T) {
		// Create a temp directory with hidden and non-hidden directories
		tmpDir, err := os.MkdirTemp("", "listdir_hidden_test")
		if err != nil {
			t.Fatalf("failed to create temp dir: %v", err)
		}
		defer os.RemoveAll(tmpDir)

		for _, name := range []string{".alpha", "beta", ".gamma", "delta", "alpha"} {
			if err := os.Mkdir(filepath.Join(tmpDir, name), 0o755); err != nil {
				t.Fatalf("failed to create dir %s: %v", name, err)
			}
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

		if len(resp.Entries) != 5 {
			t.Fatalf("expected 5 entries, got: %d", len(resp.Entries))
		}

		// Non-hidden sorted first, then hidden sorted
		want := []string{"alpha", "beta", "delta", ".alpha", ".gamma"}
		for i, e := range resp.Entries {
			if e.Name != want[i] {
				t.Errorf("entry[%d]: expected %q, got %q", i, want[i], e.Name)
			}
		}
	})

	t.Run("git_repo_head_subject", func(t *testing.T) {
		// Create a temp directory containing a git repo
		tmpDir, err := os.MkdirTemp("", "listdir_git_test")
		if err != nil {
			t.Fatalf("failed to create temp dir: %v", err)
		}
		defer os.RemoveAll(tmpDir)

		// Create a subdirectory that will be a git repo
		repoDir := tmpDir + "/myrepo"
		if err := os.Mkdir(repoDir, 0o755); err != nil {
			t.Fatalf("failed to create repo dir: %v", err)
		}

		// Initialize git repo and create a commit
		cmd := exec.Command("git", "init")
		cmd.Dir = repoDir
		if err := cmd.Run(); err != nil {
			t.Fatalf("failed to init git: %v", err)
		}

		cmd = exec.Command("git", "config", "user.email", "test@example.com")
		cmd.Dir = repoDir
		if err := cmd.Run(); err != nil {
			t.Fatalf("failed to config git email: %v", err)
		}

		cmd = exec.Command("git", "config", "user.name", "Test User")
		cmd.Dir = repoDir
		if err := cmd.Run(); err != nil {
			t.Fatalf("failed to config git name: %v", err)
		}

		// Create a file and commit it
		if err := os.WriteFile(repoDir+"/README.md", []byte("# Hello"), 0o644); err != nil {
			t.Fatalf("failed to write file: %v", err)
		}

		cmd = exec.Command("git", "add", "README.md")
		cmd.Dir = repoDir
		if err := cmd.Run(); err != nil {
			t.Fatalf("failed to git add: %v", err)
		}

		cmd = exec.Command("git", "commit", "-m", "Test commit subject line\n\nPrompt: test")
		cmd.Dir = repoDir
		if err := cmd.Run(); err != nil {
			t.Fatalf("failed to git commit: %v", err)
		}

		// Create another directory that is not a git repo
		nonRepoDir := tmpDir + "/notarepo"
		if err := os.Mkdir(nonRepoDir, 0o755); err != nil {
			t.Fatalf("failed to create non-repo dir: %v", err)
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

		if len(resp.Entries) != 2 {
			t.Fatalf("expected 2 entries, got: %d", len(resp.Entries))
		}

		// Find the git repo entry and verify it has the commit subject
		var gitEntry, nonGitEntry *DirectoryEntry
		for i := range resp.Entries {
			if resp.Entries[i].Name == "myrepo" {
				gitEntry = &resp.Entries[i]
			} else if resp.Entries[i].Name == "notarepo" {
				nonGitEntry = &resp.Entries[i]
			}
		}

		if gitEntry == nil {
			t.Fatal("expected to find myrepo entry")
		}
		if nonGitEntry == nil {
			t.Fatal("expected to find notarepo entry")
		}

		// Git repo should have the HEAD commit subject
		if gitEntry.GitHeadSubject != "Test commit subject line" {
			t.Errorf("expected git_head_subject 'Test commit subject line', got: %q", gitEntry.GitHeadSubject)
		}

		// Non-git dir should not have a subject
		if nonGitEntry.GitHeadSubject != "" {
			t.Errorf("expected empty git_head_subject for non-git dir, got: %q", nonGitEntry.GitHeadSubject)
		}
	})

	t.Run("git_worktree_root", func(t *testing.T) {
		// Create a main git repo and a worktree, then verify that
		// listing the worktree returns git_worktree_root pointing to the main repo.
		tmpDir, err := os.MkdirTemp("", "listdir_wtroot_test")
		if err != nil {
			t.Fatalf("failed to create temp dir: %v", err)
		}
		defer os.RemoveAll(tmpDir)

		mainRepo := filepath.Join(tmpDir, "main-repo")
		if err := os.Mkdir(mainRepo, 0o755); err != nil {
			t.Fatalf("failed to create main repo dir: %v", err)
		}

		for _, args := range [][]string{
			{"git", "init"},
			{"git", "config", "user.email", "test@example.com"},
			{"git", "config", "user.name", "Test User"},
		} {
			cmd := exec.Command(args[0], args[1:]...)
			cmd.Dir = mainRepo
			if err := cmd.Run(); err != nil {
				t.Fatalf("%v failed: %v", args, err)
			}
		}

		if err := os.WriteFile(filepath.Join(mainRepo, "README.md"), []byte("# Hi"), 0o644); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("git", "add", ".")
		cmd.Dir = mainRepo
		if err := cmd.Run(); err != nil {
			t.Fatal(err)
		}
		cmd = exec.Command("git", "commit", "-m", "init\n\nPrompt: test")
		cmd.Dir = mainRepo
		if err := cmd.Run(); err != nil {
			t.Fatal(err)
		}

		// Create a worktree
		cmd = exec.Command("git", "branch", "wt-branch")
		cmd.Dir = mainRepo
		if err := cmd.Run(); err != nil {
			t.Fatal(err)
		}
		worktreePath := filepath.Join(tmpDir, "my-worktree")
		cmd = exec.Command("git", "worktree", "add", worktreePath, "wt-branch")
		cmd.Dir = mainRepo
		if err := cmd.Run(); err != nil {
			t.Fatal(err)
		}

		// List the worktree directory itself - should have git_worktree_root
		req := httptest.NewRequest("GET", "/api/list-directory?path="+worktreePath, nil)
		w := httptest.NewRecorder()
		h.server.handleListDirectory(w, req)

		var resp ListDirectoryResponse
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("failed to parse response: %v", err)
		}
		if resp.GitWorktreeRoot != mainRepo {
			t.Errorf("expected git_worktree_root=%q, got %q", mainRepo, resp.GitWorktreeRoot)
		}

		// List the main repo directory - should NOT have git_worktree_root
		req = httptest.NewRequest("GET", "/api/list-directory?path="+mainRepo, nil)
		w = httptest.NewRecorder()
		h.server.handleListDirectory(w, req)

		var resp2 ListDirectoryResponse
		if err := json.Unmarshal(w.Body.Bytes(), &resp2); err != nil {
			t.Fatalf("failed to parse response: %v", err)
		}
		if resp2.GitWorktreeRoot != "" {
			t.Errorf("main repo should not have git_worktree_root, got %q", resp2.GitWorktreeRoot)
		}
	})

	t.Run("git_worktree_head_subject", func(t *testing.T) {
		// Create a temp directory containing a git repo and a worktree
		tmpDir, err := os.MkdirTemp("", "listdir_worktree_test")
		if err != nil {
			t.Fatalf("failed to create temp dir: %v", err)
		}
		defer os.RemoveAll(tmpDir)

		// Create a main git repo
		mainRepo := tmpDir + "/main-repo"
		if err := os.Mkdir(mainRepo, 0o755); err != nil {
			t.Fatalf("failed to create main repo dir: %v", err)
		}

		// Initialize git repo and create a commit
		cmd := exec.Command("git", "init")
		cmd.Dir = mainRepo
		if err := cmd.Run(); err != nil {
			t.Fatalf("failed to init git: %v", err)
		}

		cmd = exec.Command("git", "config", "user.email", "test@example.com")
		cmd.Dir = mainRepo
		if err := cmd.Run(); err != nil {
			t.Fatalf("failed to config git email: %v", err)
		}

		cmd = exec.Command("git", "config", "user.name", "Test User")
		cmd.Dir = mainRepo
		if err := cmd.Run(); err != nil {
			t.Fatalf("failed to config git name: %v", err)
		}

		// Create a file and commit it
		if err := os.WriteFile(mainRepo+"/README.md", []byte("# Hello"), 0o644); err != nil {
			t.Fatalf("failed to write file: %v", err)
		}

		cmd = exec.Command("git", "add", "README.md")
		cmd.Dir = mainRepo
		if err := cmd.Run(); err != nil {
			t.Fatalf("failed to git add: %v", err)
		}

		cmd = exec.Command("git", "commit", "-m", "Main repo commit\n\nPrompt: test")
		cmd.Dir = mainRepo
		if err := cmd.Run(); err != nil {
			t.Fatalf("failed to git commit: %v", err)
		}

		// Create a branch and worktree
		cmd = exec.Command("git", "branch", "feature-branch")
		cmd.Dir = mainRepo
		if err := cmd.Run(); err != nil {
			t.Fatalf("failed to create branch: %v", err)
		}

		worktreePath := tmpDir + "/worktree-dir"
		cmd = exec.Command("git", "worktree", "add", worktreePath, "feature-branch")
		cmd.Dir = mainRepo
		if err := cmd.Run(); err != nil {
			t.Fatalf("failed to create worktree: %v", err)
		}

		// Verify the worktree has a .git file (not directory)
		gitPath := worktreePath + "/.git"
		fi, err := os.Stat(gitPath)
		if err != nil {
			t.Fatalf("failed to stat worktree .git: %v", err)
		}
		if fi.IsDir() {
			t.Fatalf("expected .git to be a file for worktree, got directory")
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

		// Find the worktree entry and verify it has the commit subject
		var worktreeEntry *DirectoryEntry
		for i := range resp.Entries {
			if resp.Entries[i].Name == "worktree-dir" {
				worktreeEntry = &resp.Entries[i]
			}
		}

		if worktreeEntry == nil {
			t.Fatal("expected to find worktree-dir entry")
		}

		// Worktree should have the HEAD commit subject
		if worktreeEntry.GitHeadSubject != "Main repo commit" {
			t.Errorf("expected git_head_subject 'Main repo commit', got: %q", worktreeEntry.GitHeadSubject)
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
