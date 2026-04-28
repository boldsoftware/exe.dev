package inventory

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"time"
)

// GitRepo maintains a bare clone of the exe repo for resolving SHAs to commit subjects.
type GitRepo struct {
	mu      sync.RWMutex
	dir     string // path to bare repo
	log     *slog.Logger
	repoURL string

	// onChange, if set, is called with the new HEAD SHA after a fetch
	// that advances refs/heads/main. Called from the polling goroutine.
	onChange func(sha string)
	lastSHA  string // last known HEAD of main; guarded by mu
}

// NewGitRepo creates a new GitRepo service.
func NewGitRepo(log *slog.Logger, dir, repoURL string) *GitRepo {
	return &GitRepo{
		dir:     dir,
		log:     log,
		repoURL: repoURL,
	}
}

// OnChange registers a callback that fires when refs/heads/main advances.
// Must be called before Run.
func (g *GitRepo) OnChange(f func(sha string)) {
	g.onChange = f
}

// Run clones the repo if needed, then fetches every 60 seconds.
// It blocks until ctx is cancelled.
func (g *GitRepo) Run(ctx context.Context) {
	if err := g.ensureClone(ctx); err != nil {
		g.log.Error("git repo initial clone failed", "error", err)
	}

	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := g.fetch(ctx); err != nil {
				g.log.Error("git repo fetch failed", "error", err)
			}
		}
	}
}

func (g *GitRepo) ensureClone(ctx context.Context) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	// Check if already cloned.
	if _, err := os.Stat(g.dir + "/HEAD"); err == nil {
		return g.fetchLocked(ctx)
	}

	g.log.Info("cloning git repo", "url", g.repoURL, "dir", g.dir)
	cmd := exec.CommandContext(ctx, "git", "clone", "--bare", g.repoURL, g.dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git clone --bare: %w: %s", err, out)
	}

	// Bare clones don't set a fetch refspec, so git fetch only gets
	// the default branch. Fix that so we track all branches.
	cmd = exec.CommandContext(ctx, "git", "-C", g.dir, "config", "remote.origin.fetch", "+refs/heads/*:refs/heads/*")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git config fetch refspec: %w: %s", err, out)
	}
	return g.fetchLocked(ctx)
}

func (g *GitRepo) fetch(ctx context.Context) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.fetchLocked(ctx)
}

func (g *GitRepo) fetchLocked(ctx context.Context) error {
	// Bound git fetch so a slow/unresponsive remote can't hold the write
	// lock indefinitely — all read methods (HeadSHA, ResolveCommit,
	// CommitLog) take g.mu.RLock and would otherwise block behind it.
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", g.dir, "fetch", "--quiet", "origin")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git fetch: %w: %s", err, out)
	}

	// Check if HEAD of main advanced and fire the onChange callback.
	if g.onChange != nil {
		sha := g.headSHALocked(ctx)
		if sha != "" && sha != g.lastSHA {
			prev := g.lastSHA
			g.lastSHA = sha
			if prev != "" {
				// Don't fire on the initial fetch — only on actual advances.
				g.log.Info("main advanced", "from", prev[:12], "to", sha[:12])
				go g.onChange(sha)
			}
		}
	}
	return nil
}

// HeadSHA returns the current SHA of refs/heads/main, or "" on error.
func (g *GitRepo) HeadSHA() string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return g.headSHALocked(ctx)
}

// headSHALocked resolves refs/heads/main. Caller must hold mu (read or write).
func (g *GitRepo) headSHALocked(ctx context.Context) string {
	cmd := exec.CommandContext(ctx, "git", "-C", g.dir, "rev-parse", "refs/heads/main")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return string(bytes.TrimSpace(out))
}

// CommitCount returns the number of commits in (fromSHA, toSHA].
// Returns -1 on error.
func (g *GitRepo) CommitCount(fromSHA, toSHA string) int {
	g.mu.RLock()
	defer g.mu.RUnlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", g.dir, "rev-list", "--count", fromSHA+".."+toSHA)
	out, err := cmd.Output()
	if err != nil {
		return -1
	}
	n, err := strconv.Atoi(string(bytes.TrimSpace(out)))
	if err != nil {
		return -1
	}
	return n
}

// CommitInfo holds resolved metadata for a commit.
type CommitInfo struct {
	Subject       string
	Date          time.Time
	CommitsBehind int // -1 if unknown
}

// LogEntry is a single commit in a log range.
type LogEntry struct {
	SHA     string    `json:"sha"`
	Subject string    `json:"subject"`
	Date    time.Time `json:"date"`
}

// CommitLog returns up to maxN commits from (fromSHA, toSHA].
// If fromSHA is empty, returns the last maxN commits up to toSHA.
func (g *GitRepo) CommitLog(fromSHA, toSHA string, maxN int) ([]LogEntry, error) {
	g.mu.RLock()
	defer g.mu.RUnlock()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var revRange string
	if fromSHA != "" {
		revRange = fromSHA + ".." + toSHA
	} else {
		revRange = toSHA
	}
	cmd := exec.CommandContext(ctx, "git", "-C", g.dir, "log",
		"--format=%H%x00%s%x00%aI", fmt.Sprintf("-%d", maxN), revRange)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git log %s: %w", revRange, err)
	}
	out = bytes.TrimSpace(out)
	if len(out) == 0 {
		return nil, nil
	}
	var entries []LogEntry
	for _, line := range bytes.Split(out, []byte("\n")) {
		parts := bytes.SplitN(line, []byte{0}, 3)
		if len(parts) < 2 {
			continue
		}
		e := LogEntry{
			SHA:     string(parts[0]),
			Subject: string(parts[1]),
		}
		if len(parts) == 3 {
			if t, err := time.Parse(time.RFC3339, string(parts[2])); err == nil {
				e.Date = t
			}
		}
		entries = append(entries, e)
	}
	return entries, nil
}

// ResolveCommit returns metadata for the given SHA: subject, date, and
// how many commits it is behind refs/heads/main.
func (g *GitRepo) ResolveCommit(sha string) (CommitInfo, error) {
	if len(sha) != 40 {
		return CommitInfo{CommitsBehind: -1}, nil
	}
	if _, err := hex.DecodeString(sha); err != nil {
		return CommitInfo{CommitsBehind: -1}, nil
	}

	g.mu.RLock()
	defer g.mu.RUnlock()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Subject + author date (ISO 8601).
	cmd := exec.CommandContext(ctx, "git", "-C", g.dir, "log", "-1", "--format=%s%x00%aI", sha)
	out, err := cmd.Output()
	if err != nil {
		return CommitInfo{CommitsBehind: -1}, fmt.Errorf("git log for %s: %w", sha, err)
	}
	parts := bytes.SplitN(bytes.TrimSpace(out), []byte{0}, 2)
	info := CommitInfo{
		Subject:       string(parts[0]),
		CommitsBehind: -1,
	}
	if len(parts) == 2 {
		if t, err := time.Parse(time.RFC3339, string(parts[1])); err == nil {
			info.Date = t
		}
	}

	// Count commits behind main.
	cmd = exec.CommandContext(ctx, "git", "-C", g.dir, "rev-list", "--count", sha+"..refs/heads/main")
	out, err = cmd.Output()
	if err == nil {
		if n, err := strconv.Atoi(string(bytes.TrimSpace(out))); err == nil {
			info.CommitsBehind = n
		}
	}

	return info, nil
}
