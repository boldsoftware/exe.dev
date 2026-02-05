// Package gitstate provides utilities for tracking git repository state.
package gitstate

import (
	"os"
	"os/exec"
	"strings"
)

// GitState represents the current state of a git repository.
type GitState struct {
	// Worktree is the absolute path to the worktree root.
	// For regular repos, this is the same as the git root.
	// For worktrees, this is the worktree directory.
	Worktree string

	// Branch is the current branch name, or empty if detached HEAD.
	Branch string

	// Commit is the current commit hash (short form).
	Commit string

	// Subject is the commit message subject line.
	Subject string

	// IsRepo is true if the directory is inside a git repository.
	IsRepo bool
}

// GetGitState returns the git state for the given directory.
// If dir is empty, uses the current working directory.
func GetGitState(dir string) *GitState {
	state := &GitState{}

	// Get the worktree root (this works for both regular repos and worktrees)
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	if dir != "" {
		cmd.Dir = dir
	}
	output, err := cmd.Output()
	if err != nil {
		// Not in a git repository
		return state
	}
	state.IsRepo = true
	state.Worktree = strings.TrimSpace(string(output))

	// Get the current commit hash (short form)
	cmd = exec.Command("git", "rev-parse", "--short", "HEAD")
	if dir != "" {
		cmd.Dir = dir
	}
	output, err = cmd.Output()
	if err == nil {
		state.Commit = strings.TrimSpace(string(output))
	}

	// Get the commit subject line
	cmd = exec.Command("git", "log", "-1", "--format=%s")
	if dir != "" {
		cmd.Dir = dir
	}
	output, err = cmd.Output()
	if err == nil {
		state.Subject = strings.TrimSpace(string(output))
	}

	// Get the current branch name
	// First try symbolic-ref for normal branches
	cmd = exec.Command("git", "symbolic-ref", "--short", "HEAD")
	if dir != "" {
		cmd.Dir = dir
	}
	output, err = cmd.Output()
	if err == nil {
		state.Branch = strings.TrimSpace(string(output))
	}
	// If symbolic-ref fails, we're in detached HEAD state - branch stays empty

	return state
}

// Equal reports whether g and other represent the same git state.
func (g *GitState) Equal(other *GitState) bool {
	if g == nil && other == nil {
		return true
	}
	if g == nil || other == nil {
		return false
	}
	return g.Worktree == other.Worktree &&
		g.Branch == other.Branch &&
		g.Commit == other.Commit &&
		g.Subject == other.Subject &&
		g.IsRepo == other.IsRepo
}

// tildeReplace replaces the home directory prefix with ~ for display.
func tildeReplace(path string) string {
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(path, home) {
		return "~" + path[len(home):]
	}
	return path
}

// String returns a human-readable description of the git state change.
// It's designed to be shown to users, not the LLM.
func (g *GitState) String() string {
	if g == nil || !g.IsRepo {
		return ""
	}

	worktreePath := tildeReplace(g.Worktree)
	subject := g.Subject
	if len(subject) > 50 {
		subject = subject[:47] + "..."
	}

	if g.Branch != "" {
		return worktreePath + " (" + g.Branch + ") now at " + g.Commit + " \"" + subject + "\""
	}
	return worktreePath + " (detached) now at " + g.Commit + " \"" + subject + "\""
}
