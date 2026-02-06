package server

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestSystemPromptIncludesCwdGuidanceFiles verifies that AGENTS.md from the working directory
// is included in the generated system prompt.
func TestSystemPromptIncludesCwdGuidanceFiles(t *testing.T) {
	// Create a temp directory to serve as our "context directory"
	tmpDir, err := os.MkdirTemp("", "shelley_test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create an AGENTS.md file in the temp directory
	agentsContent := "TEST_UNIQUE_CONTENT_12345: Always use Go for everything."
	agentsFile := filepath.Join(tmpDir, "AGENTS.md")
	if err := os.WriteFile(agentsFile, []byte(agentsContent), 0o644); err != nil {
		t.Fatalf("failed to write AGENTS.md: %v", err)
	}

	// Generate system prompt for this directory
	prompt, err := GenerateSystemPrompt(tmpDir)
	if err != nil {
		t.Fatalf("GenerateSystemPrompt failed: %v", err)
	}

	// Verify the unique content from AGENTS.md is included in the prompt
	if !strings.Contains(prompt, "TEST_UNIQUE_CONTENT_12345") {
		t.Errorf("system prompt should contain content from AGENTS.md in the working directory")
		t.Logf("AGENTS.md content: %s", agentsContent)
		t.Logf("Generated prompt (first 2000 chars): %s", prompt[:min(len(prompt), 2000)])
	}

	// Verify the file path is mentioned in guidance section
	if !strings.Contains(prompt, agentsFile) {
		t.Errorf("system prompt should reference the AGENTS.md file path")
	}
}

// TestSystemPromptEmptyCwdFallsBackToCurrentDir verifies that an empty workingDir
// causes GenerateSystemPrompt to use the current directory.
func TestSystemPromptEmptyCwdFallsBackToCurrentDir(t *testing.T) {
	// Get current directory for comparison
	currentDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get current directory: %v", err)
	}

	// Generate system prompt with empty workingDir
	prompt, err := GenerateSystemPrompt("")
	if err != nil {
		t.Fatalf("GenerateSystemPrompt failed: %v", err)
	}

	// Verify the current directory is mentioned in the prompt
	if !strings.Contains(prompt, currentDir) {
		t.Errorf("system prompt should contain current directory when cwd is empty")
	}
}

// TestSystemPromptDetectsGitInWorkingDir verifies that the system prompt
// correctly detects a git repo in the specified working directory, not the
// process's cwd. Regression test for https://github.com/boldsoftware/shelley/issues/71
func TestSystemPromptDetectsGitInWorkingDir(t *testing.T) {
	// Create a temp dir with a git repo
	tmpDir, err := os.MkdirTemp("", "shelley_git_test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Initialize a git repo in the temp dir
	cmd := exec.Command("git", "init")
	cmd.Dir = tmpDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init failed: %v\n%s", err, out)
	}
	cmd = exec.Command("git", "commit", "--allow-empty", "-m", "initial")
	cmd.Dir = tmpDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit failed: %v\n%s", err, out)
	}

	// Generate system prompt for the git repo directory
	prompt, err := GenerateSystemPrompt(tmpDir)
	if err != nil {
		t.Fatalf("GenerateSystemPrompt failed: %v", err)
	}

	// The prompt should say "Git repository root:" not "Not in a git repository"
	if strings.Contains(prompt, "Not in a git repository") {
		t.Errorf("system prompt incorrectly says 'Not in a git repository' for a directory that is a git repo")
	}
	if !strings.Contains(prompt, "Git repository root:") {
		t.Errorf("system prompt should contain 'Git repository root:' for a git repo directory")
	}
	if !strings.Contains(prompt, tmpDir) {
		t.Errorf("system prompt should reference the git root directory %s", tmpDir)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
