package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

type RunRequest struct {
	Commit   string   `json:"commit"`
	Flags    []string `json:"flags"`
	Packages []string `json:"packages"`
}

func (r *RunRequest) validate() error {
	if r.Commit == "" {
		return fmt.Errorf("commit is required")
	}
	if len(r.Flags) == 0 {
		r.Flags = []string{"-race", "-timeout=15m", "-failfast"}
	}
	if len(r.Packages) == 0 {
		r.Packages = []string{"./e1e", "./e1e/testinfra", "./e1e/exelets"}
	}
	return nil
}

type e1edMsg struct {
	E1ED     bool   `json:"e1ed"`
	Phase    string `json:"phase"`
	Msg      string `json:"msg,omitempty"`
	ExitCode *int   `json:"exit_code,omitempty"`
	Duration string `json:"duration,omitempty"`
}

func writeMsg(w io.Writer, phase, msg string) {
	data, _ := json.Marshal(e1edMsg{E1ED: true, Phase: phase, Msg: msg})
	data = append(data, '\n')
	w.Write(data)
}

func writeDone(w io.Writer, exitCode int, duration time.Duration) {
	data, _ := json.Marshal(e1edMsg{
		E1ED:     true,
		Phase:    "done",
		ExitCode: &exitCode,
		Duration: duration.Round(time.Second).String(),
	})
	data = append(data, '\n')
	w.Write(data)
}

type RunInfo struct {
	ID      string    `json:"id"`
	Commit  string    `json:"commit"`
	Started time.Time `json:"started"`
}

// executeRun runs e1e tests for the given request, streaming output to w.
func (s *Server) executeRun(ctx context.Context, req RunRequest, w io.Writer, flush func()) (int, error) {
	start := time.Now()
	runID := fmt.Sprintf("%s-%d", req.Commit[:minInt(8, len(req.Commit))], start.UnixNano()%100000)

	s.trackRun(RunInfo{ID: runID, Commit: req.Commit, Started: start})
	defer s.untrackRun(runID)

	// 1. Verify commit exists in bare repo.
	verifyCmd := exec.CommandContext(ctx, "git", "cat-file", "-t", req.Commit)
	verifyCmd.Dir = s.repoPath
	if out, err := verifyCmd.CombinedOutput(); err != nil {
		return 1, fmt.Errorf("commit %s not found in repo: %s (%w)", req.Commit, string(out), err)
	}

	// Resolve to full SHA for worktree.
	resolveCmd := exec.CommandContext(ctx, "git", "rev-parse", req.Commit)
	resolveCmd.Dir = s.repoPath
	shaOut, err := resolveCmd.Output()
	if err != nil {
		return 1, fmt.Errorf("resolve commit %s: %w", req.Commit, err)
	}
	fullSHA := trimNewline(string(shaOut))

	// 2. Create worktree.
	worktree, err := os.MkdirTemp("", "e1ed-run-")
	if err != nil {
		return 1, fmt.Errorf("create temp dir: %w", err)
	}
	defer func() {
		// Clean up worktree.
		rmCmd := exec.Command("git", "worktree", "remove", "--force", worktree)
		rmCmd.Dir = s.repoPath
		if err := rmCmd.Run(); err != nil {
			slog.ErrorContext(ctx, "remove worktree", "path", worktree, "err", err)
			os.RemoveAll(worktree)
		}
	}()

	wtCmd := exec.CommandContext(ctx, "git", "worktree", "add", "--detach", worktree, fullSHA)
	wtCmd.Dir = s.repoPath
	if out, err := wtCmd.CombinedOutput(); err != nil {
		return 1, fmt.Errorf("git worktree add: %s (%w)", string(out), err)
	}
	writeMsg(w, "setup", fmt.Sprintf("created worktree at %s", worktree))
	flush()

	// 3. Claim a VM from the pool.
	writeMsg(w, "setup", "waiting for environment...")
	flush()

	claimCtx, claimCancel := context.WithTimeout(ctx, 20*time.Minute)
	defer claimCancel()

	vm, slotIdx, err := s.pool.Claim(claimCtx, runID)
	if err != nil {
		return 1, fmt.Errorf("claim VM: %w", err)
	}
	defer s.pool.Release(slotIdx)

	writeMsg(w, "setup", fmt.Sprintf("claimed environment %d (ssh://%s@%s)", slotIdx, vm.User, vm.IP))
	flush()

	// 4. Run make exelet-fs in worktree.
	writeMsg(w, "setup", "running make exelet-fs")
	flush()

	makeCmd := exec.CommandContext(ctx, "make", "exelet-fs")
	makeCmd.Dir = worktree
	makeCmd.Env = append(os.Environ(), "GOARCH=amd64", "GOOS=linux")
	if out, err := makeCmd.CombinedOutput(); err != nil {
		writeMsg(w, "error", fmt.Sprintf("make exelet-fs failed: %s", string(out)))
		flush()
		return 1, fmt.Errorf("make exelet-fs: %w", err)
	}

	// 5. Run go test -json.
	writeMsg(w, "test", "starting go test")
	flush()

	args := []string{"test", "-json"}
	args = append(args, req.Flags...)
	args = append(args, req.Packages...)

	testCmd := exec.CommandContext(ctx, "go", args...)
	testCmd.Dir = worktree
	testCmd.Env = append(os.Environ(),
		"CI=true",
		"GITHUB_ACTIONS=false",
		fmt.Sprintf("CTR_HOST=ssh://%s@%s", vm.User, vm.IP),
	)

	stdout, err := testCmd.StdoutPipe()
	if err != nil {
		return 1, fmt.Errorf("stdout pipe: %w", err)
	}
	testCmd.Stderr = testCmd.Stdout // merge stderr into stdout

	if err := testCmd.Start(); err != nil {
		return 1, fmt.Errorf("start go test: %w", err)
	}

	// 6. Stream output line by line.
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		w.Write(line)
		w.Write([]byte{'\n'})
		flush()
	}

	// 7. Collect exit code.
	exitCode := 0
	if err := testCmd.Wait(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = 1
		}
	}

	duration := time.Since(start)
	writeDone(w, exitCode, duration)
	flush()

	slog.InfoContext(ctx, "run completed", "run", runID, "exit_code", exitCode, "duration", duration)
	return exitCode, nil
}

func trimNewline(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// resolveOpsDir resolves the ops/ directory from the bare repo by creating
// or updating a permanent worktree.
func resolveOpsDir(repoPath string) (string, error) {
	opsWorktree := filepath.Join(repoPath, "ops-worktree")

	// Resolve HEAD to a concrete SHA from the bare repo.
	headCmd := exec.Command("git", "rev-parse", "HEAD")
	headCmd.Dir = repoPath
	headOut, err := headCmd.Output()
	if err != nil {
		return "", fmt.Errorf("resolve bare repo HEAD: %w", err)
	}
	headSHA := trimNewline(string(headOut))

	// Check if worktree already exists.
	if _, err := os.Stat(filepath.Join(opsWorktree, ".git")); err == nil {
		// Update to latest HEAD.
		cmd := exec.Command("git", "checkout", "--detach", headSHA)
		cmd.Dir = opsWorktree
		if out, err := cmd.CombinedOutput(); err != nil {
			slog.Warn("update ops worktree", "err", err, "output", string(out))
		}
		return filepath.Join(opsWorktree, "ops"), nil
	}

	// Create the worktree.
	cmd := exec.Command("git", "worktree", "add", "--detach", opsWorktree, headSHA)
	cmd.Dir = repoPath
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("create ops worktree: %s (%w)", string(out), err)
	}
	return filepath.Join(opsWorktree, "ops"), nil
}
