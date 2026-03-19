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
}

// NewGitRepo creates a new GitRepo service.
func NewGitRepo(log *slog.Logger, dir, repoURL string) *GitRepo {
	return &GitRepo{
		dir:     dir,
		log:     log,
		repoURL: repoURL,
	}
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
	cmd := exec.CommandContext(ctx, "git", "-C", g.dir, "fetch", "--quiet", "origin")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git fetch: %w: %s", err, out)
	}
	return nil
}

// HeadSHA returns the current SHA of refs/heads/main, or "" on error.
func (g *GitRepo) HeadSHA() string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", g.dir, "rev-parse", "refs/heads/main")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return string(bytes.TrimSpace(out))
}

// CommitInfo holds resolved metadata for a commit.
type CommitInfo struct {
	Subject       string
	Date          time.Time
	CommitsBehind int // -1 if unknown
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
