package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestGetGitRoot tests the getGitRoot function
func TestGetGitRoot(t *testing.T) {
	// Create a temporary directory for testing
	tempDir := t.TempDir()

	// Test with non-git directory
	_, err := getGitRoot(tempDir)
	if err == nil {
		t.Error("expected error for non-git directory, got nil")
	}

	// Create a git repository
	gitDir := filepath.Join(tempDir, "repo")
	err = os.MkdirAll(gitDir, 0o755)
	if err != nil {
		t.Fatal(err)
	}

	// Initialize git repo
	cmd := exec.Command("git", "init")
	cmd.Dir = gitDir
	err = cmd.Run()
	if err != nil {
		t.Fatal(err)
	}

	// Configure git user for commits
	cmd = exec.Command("git", "config", "user.name", "Test User")
	cmd.Dir = gitDir
	err = cmd.Run()
	if err != nil {
		t.Fatal(err)
	}

	cmd = exec.Command("git", "config", "user.email", "test@example.com")
	cmd.Dir = gitDir
	err = cmd.Run()
	if err != nil {
		t.Fatal(err)
	}

	// Test with git directory
	root, err := getGitRoot(gitDir)
	if err != nil {
		t.Errorf("unexpected error for git directory: %v", err)
	}
	if root != gitDir {
		t.Errorf("expected root %s, got %s", gitDir, root)
	}

	// Test with subdirectory of git directory
	subDir := filepath.Join(gitDir, "subdir")
	err = os.MkdirAll(subDir, 0o755)
	if err != nil {
		t.Fatal(err)
	}

	root, err = getGitRoot(subDir)
	if err != nil {
		t.Errorf("unexpected error for git subdirectory: %v", err)
	}
	if root != gitDir {
		t.Errorf("expected root %s, got %s", gitDir, root)
	}
}

// TestParseDiffStat tests the parseDiffStat function
func TestParseDiffStat(t *testing.T) {
	// Test empty output
	additions, deletions, filesCount := parseDiffStat("")
	if additions != 0 || deletions != 0 || filesCount != 0 {
		t.Errorf("expected 0,0,0 for empty output, got %d,%d,%d", additions, deletions, filesCount)
	}

	// Test single file
	output := "5\t3\tfile1.txt\n"
	additions, deletions, filesCount = parseDiffStat(output)
	if additions != 5 || deletions != 3 || filesCount != 1 {
		t.Errorf("expected 5,3,1 for single file, got %d,%d,%d", additions, deletions, filesCount)
	}

	// Test multiple files
	output = "5\t3\tfile1.txt\n10\t2\tfile2.txt\n"
	additions, deletions, filesCount = parseDiffStat(output)
	if additions != 15 || deletions != 5 || filesCount != 2 {
		t.Errorf("expected 15,5,2 for multiple files, got %d,%d,%d", additions, deletions, filesCount)
	}

	// Test file with additions only
	output = "5\t0\tfile1.txt\n"
	additions, deletions, filesCount = parseDiffStat(output)
	if additions != 5 || deletions != 0 || filesCount != 1 {
		t.Errorf("expected 5,0,1 for additions only, got %d,%d,%d", additions, deletions, filesCount)
	}

	// Test file with deletions only
	output = "0\t3\tfile1.txt\n"
	additions, deletions, filesCount = parseDiffStat(output)
	if additions != 0 || deletions != 3 || filesCount != 1 {
		t.Errorf("expected 0,3,1 for deletions only, got %d,%d,%d", additions, deletions, filesCount)
	}

	// Test file with binary content (represented as -)
	output = "-\t-\tfile1.bin\n"
	additions, deletions, filesCount = parseDiffStat(output)
	if additions != 0 || deletions != 0 || filesCount != 1 {
		t.Errorf("expected 0,0,1 for binary file, got %d,%d,%d", additions, deletions, filesCount)
	}
}

// setupTestGitRepo creates a temporary git repository with some content for testing
func setupTestGitRepo(t *testing.T) string {
	// Create a temporary directory for testing
	tempDir := t.TempDir()

	// Initialize git repo
	cmd := exec.Command("git", "init")
	cmd.Dir = tempDir
	err := cmd.Run()
	if err != nil {
		t.Fatal(err)
	}

	// Configure git user for commits
	cmd = exec.Command("git", "config", "user.name", "Test User")
	cmd.Dir = tempDir
	err = cmd.Run()
	if err != nil {
		t.Fatal(err)
	}

	cmd = exec.Command("git", "config", "user.email", "test@example.com")
	cmd.Dir = tempDir
	err = cmd.Run()
	if err != nil {
		t.Fatal(err)
	}

	// Create and commit a file
	filePath := filepath.Join(tempDir, "test.txt")
	content := "Hello, World!\n"
	err = os.WriteFile(filePath, []byte(content), 0o644)
	if err != nil {
		t.Fatal(err)
	}

	cmd = exec.Command("git", "add", "test.txt")
	cmd.Dir = tempDir
	err = cmd.Run()
	if err != nil {
		t.Fatal(err)
	}

	cmd = exec.Command("git", "commit", "-m", "Initial commit\n\nPrompt: Initial test commit for git handlers test", "--author=Test <test@example.com>")
	cmd.Dir = tempDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("git commit failed: %v", err)
		t.Logf("git commit output: %s", string(output))
		t.Fatal(err)
	}

	// Modify the file (staged changes)
	newContent := "Hello, World!\nModified content\n"
	err = os.WriteFile(filePath, []byte(newContent), 0o644)
	if err != nil {
		t.Fatal(err)
	}

	cmd = exec.Command("git", "add", "test.txt")
	cmd.Dir = tempDir
	err = cmd.Run()
	if err != nil {
		t.Fatal(err)
	}

	// Modify the file again (unstaged changes)
	unstagedContent := "Hello, World!\nModified content\nMore changes\n"
	err = os.WriteFile(filePath, []byte(unstagedContent), 0o644)
	if err != nil {
		t.Fatal(err)
	}

	// Create another file (untracked)
	untrackedPath := filepath.Join(tempDir, "untracked.txt")
	untrackedContent := "Untracked file\n"
	err = os.WriteFile(untrackedPath, []byte(untrackedContent), 0o644)
	if err != nil {
		t.Fatal(err)
	}

	return tempDir
}

// TestHandleGitDiffs tests the handleGitDiffs function
func TestHandleGitDiffs(t *testing.T) {
	h := NewTestHarness(t)

	// Test with non-git directory
	req := httptest.NewRequest("GET", "/api/git/diffs?cwd=/tmp", nil)
	w := httptest.NewRecorder()
	h.server.handleGitDiffs(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status 400 for non-git directory, got %d", w.Code)
	}

	// Setup a test git repository
	gitDir := setupTestGitRepo(t)

	// Test with valid git directory
	req = httptest.NewRequest("GET", fmt.Sprintf("/api/git/diffs?cwd=%s", gitDir), nil)
	w = httptest.NewRecorder()
	h.server.handleGitDiffs(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200 for git directory, got %d: %s", w.Code, w.Body.String())
	}

	// Check response content type
	if w.Header().Get("Content-Type") != "application/json" {
		t.Errorf("expected content-type application/json, got %s", w.Header().Get("Content-Type"))
	}

	// Parse response
	var response struct {
		Diffs   []GitDiffInfo `json:"diffs"`
		GitRoot string        `json:"gitRoot"`
	}
	err := json.Unmarshal(w.Body.Bytes(), &response)
	if err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	// Check that we have at least one diff (working changes)
	if len(response.Diffs) == 0 {
		t.Error("expected at least one diff (working changes)")
	}

	// Check that the first diff is working changes
	if len(response.Diffs) > 0 {
		diff := response.Diffs[0]
		if diff.ID != "working" {
			t.Errorf("expected first diff ID to be 'working', got %s", diff.ID)
		}
		if diff.Message != "Working Changes" {
			t.Errorf("expected first diff message to be 'Working Changes', got %s", diff.Message)
		}
	}

	// Check that git root is correct
	if response.GitRoot != gitDir {
		t.Errorf("expected git root %s, got %s", gitDir, response.GitRoot)
	}

	// Test with subdirectory of git directory
	subDir := filepath.Join(gitDir, "subdir")
	err = os.MkdirAll(subDir, 0o755)
	if err != nil {
		t.Fatal(err)
	}

	req = httptest.NewRequest("GET", fmt.Sprintf("/api/git/diffs?cwd=%s", subDir), nil)
	w = httptest.NewRecorder()
	h.server.handleGitDiffs(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200 for git subdirectory, got %d: %s", w.Code, w.Body.String())
	}
}

// TestHandleGitDiffFiles tests the handleGitDiffFiles function
func TestHandleGitDiffFiles(t *testing.T) {
	h := NewTestHarness(t)

	// Setup a test git repository
	gitDir := setupTestGitRepo(t)

	// Test with invalid method
	req := httptest.NewRequest("POST", fmt.Sprintf("/api/git/diffs/working/files?cwd=%s", gitDir), nil)
	w := httptest.NewRecorder()
	h.server.handleGitDiffFiles(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected status 405 for invalid method, got %d", w.Code)
	}

	// Test with invalid path
	req = httptest.NewRequest("GET", fmt.Sprintf("/api/git/diffs/working?cwd=%s", gitDir), nil)
	w = httptest.NewRecorder()
	h.server.handleGitDiffFiles(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status 400 for invalid path, got %d", w.Code)
	}

	// Test with non-git directory
	req = httptest.NewRequest("GET", "/api/git/diffs/working/files?cwd=/tmp", nil)
	w = httptest.NewRecorder()
	h.server.handleGitDiffFiles(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status 400 for non-git directory, got %d", w.Code)
	}

	// Test with working changes
	req = httptest.NewRequest("GET", fmt.Sprintf("/api/git/diffs/working/files?cwd=%s", gitDir), nil)
	w = httptest.NewRecorder()
	h.server.handleGitDiffFiles(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200 for working changes, got %d: %s", w.Code, w.Body.String())
	}

	// Check response content type
	if w.Header().Get("Content-Type") != "application/json" {
		t.Errorf("expected content-type application/json, got %s", w.Header().Get("Content-Type"))
	}

	// Parse response
	var files []GitFileInfo
	err := json.Unmarshal(w.Body.Bytes(), &files)
	if err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	// Check that we have at least one file
	if len(files) == 0 {
		t.Error("expected at least one file in working changes")
	}

	// Check file information
	if len(files) > 0 {
		file := files[0]
		if file.Path != "test.txt" {
			t.Errorf("expected file path test.txt, got %s", file.Path)
		}
		if file.Status != "modified" {
			t.Errorf("expected file status modified, got %s", file.Status)
		}
	}
}

// TestHandleGitFileDiff tests the handleGitFileDiff function
func TestHandleGitFileDiff(t *testing.T) {
	h := NewTestHarness(t)

	// Setup a test git repository
	gitDir := setupTestGitRepo(t)

	// Test with invalid method
	req := httptest.NewRequest("POST", fmt.Sprintf("/api/git/file-diff/working/test.txt?cwd=%s", gitDir), nil)
	w := httptest.NewRecorder()
	h.server.handleGitFileDiff(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected status 405 for invalid method, got %d", w.Code)
	}

	// Test with invalid path (missing diff ID)
	req = httptest.NewRequest("GET", fmt.Sprintf("/api/git/file-diff/test.txt?cwd=%s", gitDir), nil)
	w = httptest.NewRecorder()
	h.server.handleGitFileDiff(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status 400 for invalid path, got %d", w.Code)
	}

	// Test with non-git directory
	req = httptest.NewRequest("GET", "/api/git/file-diff/working/test.txt?cwd=/tmp", nil)
	w = httptest.NewRecorder()
	h.server.handleGitFileDiff(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status 400 for non-git directory, got %d", w.Code)
	}

	// Test with working changes
	req = httptest.NewRequest("GET", fmt.Sprintf("/api/git/file-diff/working/test.txt?cwd=%s", gitDir), nil)
	w = httptest.NewRecorder()
	h.server.handleGitFileDiff(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200 for working changes, got %d: %s", w.Code, w.Body.String())
	}

	// Check response content type
	if w.Header().Get("Content-Type") != "application/json" {
		t.Errorf("expected content-type application/json, got %s", w.Header().Get("Content-Type"))
	}

	// Parse response
	var fileDiff GitFileDiff
	err := json.Unmarshal(w.Body.Bytes(), &fileDiff)
	if err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	// Check file information
	if fileDiff.Path != "test.txt" {
		t.Errorf("expected file path test.txt, got %s", fileDiff.Path)
	}

	// Check that we have content
	if fileDiff.OldContent == "" {
		t.Error("expected old content")
	}

	if fileDiff.NewContent == "" {
		t.Error("expected new content")
	}

	// Test with path traversal attempt (should be blocked)
	req = httptest.NewRequest("GET", fmt.Sprintf("/api/git/file-diff/working/../etc/passwd?cwd=%s", gitDir), nil)
	w = httptest.NewRecorder()
	h.server.handleGitFileDiff(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status 400 for path traversal attempt, got %d", w.Code)
	}
}

// setupRootCommitRepo creates a git repo with only a single (root) commit.
func setupRootCommitRepo(t *testing.T) string {
	t.Helper()
	tempDir := t.TempDir()

	for _, args := range [][]string{
		{"init"},
		{"config", "user.name", "Test User"},
		{"config", "user.email", "test@example.com"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = tempDir
		if err := cmd.Run(); err != nil {
			t.Fatal(err)
		}
	}

	// Create files and commit
	err := os.WriteFile(filepath.Join(tempDir, "hello.txt"), []byte("hello world\n"), 0o644)
	if err != nil {
		t.Fatal(err)
	}
	err = os.WriteFile(filepath.Join(tempDir, "readme.md"), []byte("# Test\n"), 0o644)
	if err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("git", "add", "hello.txt", "readme.md")
	cmd.Dir = tempDir
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}

	cmd = exec.Command("git", "commit", "-m", "Initial commit\n\nPrompt: test", "--author=Test <test@example.com>")
	cmd.Dir = tempDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v\n%s", err, out)
	}

	return tempDir
}

func TestRootCommitDiffs(t *testing.T) {
	h := NewTestHarness(t)
	gitDir := setupRootCommitRepo(t)

	// Get the commit hash
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = gitDir
	hashBytes, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	commitHash := string(hashBytes[:len(hashBytes)-1]) // trim newline

	// handleGitDiffs should list the root commit with correct stats
	req := httptest.NewRequest("GET", fmt.Sprintf("/api/git/diffs?cwd=%s", gitDir), nil)
	w := httptest.NewRecorder()
	h.server.handleGitDiffs(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("handleGitDiffs: %d: %s", w.Code, w.Body.String())
	}

	var diffsResp struct {
		Diffs []GitDiffInfo `json:"diffs"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &diffsResp); err != nil {
		t.Fatal(err)
	}
	// Should have working + 1 commit
	if len(diffsResp.Diffs) != 2 {
		t.Fatalf("expected 2 diffs, got %d", len(diffsResp.Diffs))
	}
	commitDiff := diffsResp.Diffs[1]
	if commitDiff.FilesCount != 2 {
		t.Errorf("expected 2 files in root commit, got %d", commitDiff.FilesCount)
	}
	if commitDiff.Additions != 2 {
		t.Errorf("expected 2 additions in root commit, got %d", commitDiff.Additions)
	}

	// handleGitDiffFiles should list files from root commit
	req = httptest.NewRequest("GET", fmt.Sprintf("/api/git/diffs/%s/files?cwd=%s", commitHash, gitDir), nil)
	w = httptest.NewRecorder()
	h.server.handleGitDiffFiles(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("handleGitDiffFiles: %d: %s", w.Code, w.Body.String())
	}

	var files []GitFileInfo
	if err := json.Unmarshal(w.Body.Bytes(), &files); err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(files))
	}
	for _, f := range files {
		if f.Status != "added" {
			t.Errorf("expected status 'added' for %s in root commit, got %s", f.Path, f.Status)
		}
	}

	// handleGitFileDiff should return empty old content and correct new content
	req = httptest.NewRequest("GET", fmt.Sprintf("/api/git/file-diff/%s/hello.txt?cwd=%s", commitHash, gitDir), nil)
	w = httptest.NewRecorder()
	h.server.handleGitFileDiff(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("handleGitFileDiff: %d: %s", w.Code, w.Body.String())
	}

	var fileDiff GitFileDiff
	if err := json.Unmarshal(w.Body.Bytes(), &fileDiff); err != nil {
		t.Fatal(err)
	}
	if fileDiff.OldContent != "" {
		t.Errorf("expected empty old content for root commit, got %q", fileDiff.OldContent)
	}
	if fileDiff.NewContent != "hello world\n" {
		t.Errorf("expected 'hello world\n' as new content, got %q", fileDiff.NewContent)
	}
}
