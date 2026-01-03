package gitstate

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGetGitState_NotARepo(t *testing.T) {
	tmpDir := t.TempDir()

	state := GetGitState(tmpDir)

	if state.IsRepo {
		t.Error("expected IsRepo to be false for non-repo directory")
	}
	if state.Worktree != "" {
		t.Errorf("expected empty Worktree, got %q", state.Worktree)
	}
	if state.Branch != "" {
		t.Errorf("expected empty Branch, got %q", state.Branch)
	}
	if state.Commit != "" {
		t.Errorf("expected empty Commit, got %q", state.Commit)
	}
}

func TestGetGitState_RegularRepo(t *testing.T) {
	tmpDir := t.TempDir()

	// Initialize a git repo
	runGit(t, tmpDir, "init")
	runGit(t, tmpDir, "config", "user.email", "test@test.com")
	runGit(t, tmpDir, "config", "user.name", "Test")

	// Create a commit
	testFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, tmpDir, "add", ".")
	runGit(t, tmpDir, "commit", "-m", "initial")

	state := GetGitState(tmpDir)

	if !state.IsRepo {
		t.Error("expected IsRepo to be true")
	}
	if state.Worktree != tmpDir {
		t.Errorf("expected Worktree %q, got %q", tmpDir, state.Worktree)
	}
	// Default branch might be master or main depending on git config
	if state.Branch != "master" && state.Branch != "main" {
		t.Errorf("expected Branch 'master' or 'main', got %q", state.Branch)
	}
	if state.Commit == "" {
		t.Error("expected non-empty Commit")
	}
	if len(state.Commit) < 7 {
		t.Errorf("expected short commit hash, got %q", state.Commit)
	}
}

func TestGetGitState_Worktree(t *testing.T) {
	tmpDir := t.TempDir()
	mainRepo := filepath.Join(tmpDir, "main")
	worktreeDir := filepath.Join(tmpDir, "worktree")

	// Create main repo
	if err := os.MkdirAll(mainRepo, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, mainRepo, "init")
	runGit(t, mainRepo, "config", "user.email", "test@test.com")
	runGit(t, mainRepo, "config", "user.name", "Test")

	// Create initial commit
	testFile := filepath.Join(mainRepo, "test.txt")
	if err := os.WriteFile(testFile, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, mainRepo, "add", ".")
	runGit(t, mainRepo, "commit", "-m", "initial")

	// Create a worktree on a new branch
	runGit(t, mainRepo, "worktree", "add", "-b", "feature", worktreeDir)

	// Check state in main repo
	mainState := GetGitState(mainRepo)
	if !mainState.IsRepo {
		t.Error("expected main repo IsRepo to be true")
	}
	if mainState.Worktree != mainRepo {
		t.Errorf("expected main Worktree %q, got %q", mainRepo, mainState.Worktree)
	}

	// Check state in worktree
	worktreeState := GetGitState(worktreeDir)
	if !worktreeState.IsRepo {
		t.Error("expected worktree IsRepo to be true")
	}
	if worktreeState.Worktree != worktreeDir {
		t.Errorf("expected worktree Worktree %q, got %q", worktreeDir, worktreeState.Worktree)
	}
	if worktreeState.Branch != "feature" {
		t.Errorf("expected worktree Branch 'feature', got %q", worktreeState.Branch)
	}

	// Both should have the same commit (initially)
	if mainState.Commit != worktreeState.Commit {
		t.Errorf("expected same commit, got main=%q worktree=%q", mainState.Commit, worktreeState.Commit)
	}
}

func TestGetGitState_DetachedHead(t *testing.T) {
	tmpDir := t.TempDir()

	// Initialize and create commits
	runGit(t, tmpDir, "init")
	runGit(t, tmpDir, "config", "user.email", "test@test.com")
	runGit(t, tmpDir, "config", "user.name", "Test")

	testFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, tmpDir, "add", ".")
	runGit(t, tmpDir, "commit", "-m", "initial")

	// Get the commit hash
	commit := strings.TrimSpace(runGitOutput(t, tmpDir, "rev-parse", "HEAD"))

	// Checkout to detached HEAD
	runGit(t, tmpDir, "checkout", commit)

	state := GetGitState(tmpDir)

	if !state.IsRepo {
		t.Error("expected IsRepo to be true")
	}
	if state.Branch != "" {
		t.Errorf("expected empty Branch for detached HEAD, got %q", state.Branch)
	}
	if state.Commit == "" {
		t.Error("expected non-empty Commit")
	}
}

func TestGitState_Equal(t *testing.T) {
	tests := []struct {
		name     string
		a        *GitState
		b        *GitState
		expected bool
	}{
		{"both nil", nil, nil, true},
		{"one nil", &GitState{}, nil, false},
		{"other nil", nil, &GitState{}, false},
		{"both empty", &GitState{}, &GitState{}, true},
		{"same values", &GitState{Worktree: "/foo", Branch: "main", Commit: "abc123", IsRepo: true}, &GitState{Worktree: "/foo", Branch: "main", Commit: "abc123", IsRepo: true}, true},
		{"different worktree", &GitState{Worktree: "/foo", Branch: "main", Commit: "abc123", IsRepo: true}, &GitState{Worktree: "/bar", Branch: "main", Commit: "abc123", IsRepo: true}, false},
		{"different branch", &GitState{Worktree: "/foo", Branch: "main", Commit: "abc123", IsRepo: true}, &GitState{Worktree: "/foo", Branch: "dev", Commit: "abc123", IsRepo: true}, false},
		{"different commit", &GitState{Worktree: "/foo", Branch: "main", Commit: "abc123", IsRepo: true}, &GitState{Worktree: "/foo", Branch: "main", Commit: "def456", IsRepo: true}, false},
		{"different IsRepo", &GitState{Worktree: "/foo", Branch: "main", Commit: "abc123", IsRepo: true}, &GitState{Worktree: "/foo", Branch: "main", Commit: "abc123", IsRepo: false}, false},
		{"different subject", &GitState{Worktree: "/foo", Branch: "main", Commit: "abc123", Subject: "fix bug", IsRepo: true}, &GitState{Worktree: "/foo", Branch: "main", Commit: "abc123", Subject: "add feature", IsRepo: true}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.a.Equal(tt.b); got != tt.expected {
				t.Errorf("Equal() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestGitState_String(t *testing.T) {
	tests := []struct {
		name     string
		state    *GitState
		expected string
	}{
		{"nil state", nil, ""},
		{"not a repo", &GitState{IsRepo: false}, ""},
		{"with branch", &GitState{Worktree: "/home/user/myrepo", Branch: "main", Commit: "abc1234", IsRepo: true}, "myrepo/main now at abc1234"},
		{"detached head", &GitState{Worktree: "/home/user/myrepo", Branch: "", Commit: "abc1234", IsRepo: true}, "myrepo (detached) now at abc1234"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.state.String(); got != tt.expected {
				t.Errorf("String() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	// For commits, use --no-verify to skip hooks
	if len(args) > 0 && args[0] == "commit" {
		newArgs := []string{"commit", "--no-verify"}
		newArgs = append(newArgs, args[1:]...)
		args = newArgs
	}
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, output)
	}
}

func runGitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v failed: %v", args, err)
	}
	return string(output)
}
