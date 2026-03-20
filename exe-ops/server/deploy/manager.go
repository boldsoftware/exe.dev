package deploy

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const maxHistory = 100

// Manager orchestrates deploys. It enforces that only one deploy is
// active per (stage, role, process, host), and that only one build
// runs at a time per (process, sha).
type Manager struct {
	ctx context.Context // server lifetime

	mu      sync.Mutex
	deploys []*deploy              // all deploys, most recent first
	active  map[string]*deploy     // key: activeKey()
	builds  map[string]*sync.Mutex // key: process/sha

	repoDir  string // bare git clone (shared with inventory)
	cacheDir string // artifact cache root
	log      *slog.Logger
	client   *http.Client
}

// NewManager creates a deploy manager.
// repoDir is the bare git clone path. cacheDir is where built artifacts are cached.
func NewManager(ctx context.Context, log *slog.Logger, repoDir, cacheDir string) *Manager {
	// Resolve to absolute paths so they work regardless of exec.Cmd.Dir.
	if abs, err := filepath.Abs(repoDir); err == nil {
		repoDir = abs
	}
	if abs, err := filepath.Abs(cacheDir); err == nil {
		cacheDir = abs
	}
	return &Manager{
		ctx:      ctx,
		active:   make(map[string]*deploy),
		builds:   make(map[string]*sync.Mutex),
		repoDir:  repoDir,
		cacheDir: cacheDir,
		log:      log,
		client: &http.Client{
			Timeout: 10 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			},
		},
	}
}

// Request describes a deploy to start.
type Request struct {
	Stage   string `json:"stage"`
	Role    string `json:"role"`
	Process string `json:"process"`
	Host    string `json:"host"`     // display hostname
	DNSName string `json:"dns_name"` // tailscale DNS for SSH
	SHA     string `json:"sha"`      // 40-char hex
}

// Start begins a new deploy. Returns an error if a deploy is already
// active for the same target or the process type is unknown.
func (m *Manager) Start(req Request) (Status, error) {
	if req.Stage != "staging" && req.Stage != "global" {
		return Status{}, fmt.Errorf("only staging and global deploys are currently allowed")
	}
	if _, ok := Recipes[req.Process]; !ok {
		return Status{}, fmt.Errorf("unknown process %q", req.Process)
	}
	if len(req.SHA) != 40 {
		return Status{}, fmt.Errorf("sha must be 40 hex characters")
	}

	id := generateID()
	d := newDeploy(id, req.Stage, req.Role, req.Process, req.Host, req.DNSName, req.SHA)

	m.mu.Lock()
	key := d.activeKey()
	if existing, ok := m.active[key]; ok {
		m.mu.Unlock()
		return Status{}, fmt.Errorf("deploy already active for %s (id %s)", key, existing.id)
	}
	m.active[key] = d
	m.deploys = append([]*deploy{d}, m.deploys...)
	if len(m.deploys) > maxHistory {
		m.deploys = m.deploys[:maxHistory]
	}
	m.mu.Unlock()

	m.log.Info("deploy started",
		"id", id, "process", req.Process,
		"host", req.Host, "sha", req.SHA[:12])

	go m.execute(m.ctx, d)
	return d.snapshot(), nil
}

// List returns snapshots of all deploys, most recent first.
func (m *Manager) List() []Status {
	m.mu.Lock()
	deploys := make([]*deploy, len(m.deploys))
	copy(deploys, m.deploys)
	m.mu.Unlock()

	out := make([]Status, len(deploys))
	for i, d := range deploys {
		out[i] = d.snapshot()
	}
	return out
}

// Get returns a snapshot of a deploy by ID.
func (m *Manager) Get(id string) (Status, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, d := range m.deploys {
		if d.id == id {
			return d.snapshot(), true
		}
	}
	return Status{}, false
}

// execute runs the full deploy pipeline as a goroutine.
func (m *Manager) execute(ctx context.Context, d *deploy) {
	defer m.finish(d)

	recipe := Recipes[d.process]

	// Build (checkout + compile, cached and deduplicated).
	d.beginStep("build")
	artifact, buildOutput, err := m.ensureArtifact(ctx, d.process, d.sha, recipe)
	if buildOutput != "" {
		d.setStepOutput(buildOutput)
	}
	if d.stepDone(err) {
		return
	}

	// Upload binary to target machine.
	d.beginStep("upload")
	remotePath, err := m.upload(ctx, d, recipe, artifact)
	if d.stepDone(err) {
		return
	}

	// Update symlink to point to the new binary.
	d.beginStep("install")
	err = m.install(ctx, d, recipe, remotePath)
	if d.stepDone(err) {
		return
	}

	// Restart the systemd service.
	d.beginStep("restart")
	err = m.restart(ctx, d, recipe)
	if d.stepDone(err) {
		return
	}

	// Verify the new SHA is running (skip if no health endpoint).
	d.beginStep("verify")
	if recipe.HealthPath != "" {
		err = m.verify(ctx, d, recipe)
	} else {
		d.setStepOutput("skipped (no health endpoint)")
	}
	if d.stepDone(err) {
		return
	}

	d.complete()
	m.log.Info("deploy complete", "id", d.id, "process", d.process, "host", d.host)
}

func (m *Manager) finish(d *deploy) {
	m.mu.Lock()
	delete(m.active, d.activeKey())
	m.mu.Unlock()
}

// ensureArtifact returns the path to a built binary and a human summary,
// using the cache and serializing concurrent builds for the same (process, sha).
func (m *Manager) ensureArtifact(ctx context.Context, process, sha string, recipe Recipe) (string, string, error) {
	artifactPath := filepath.Join(m.cacheDir, process, sha, recipe.BinaryName)

	// Fast path: already cached.
	if info, err := os.Stat(artifactPath); err == nil {
		m.log.Info("using cached artifact", "process", process, "sha", sha[:12])
		return artifactPath, fmt.Sprintf("cached (%s)", formatBytes(info.Size())), nil
	}

	// Serialize builds for this (process, sha).
	buildLock := m.getBuildLock(process + "/" + sha)
	buildLock.Lock()
	defer buildLock.Unlock()

	// Double-check after acquiring lock.
	if info, err := os.Stat(artifactPath); err == nil {
		return artifactPath, fmt.Sprintf("cached (%s)", formatBytes(info.Size())), nil
	}

	return m.buildArtifact(ctx, process, sha, recipe, artifactPath)
}

func (m *Manager) getBuildLock(key string) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	mu, ok := m.builds[key]
	if !ok {
		mu = &sync.Mutex{}
		m.builds[key] = mu
	}
	return mu
}

func (m *Manager) buildArtifact(ctx context.Context, process, sha string, recipe Recipe, outputPath string) (string, string, error) {
	// Clone from the bare repo with --shared (uses hardlinks, fast)
	// so Go's VCS detection embeds vcs.revision in the binary.
	workdir, err := os.MkdirTemp("", "deploy-"+process+"-"+sha[:12]+"-*")
	if err != nil {
		return "", "", fmt.Errorf("create workdir: %w", err)
	}
	defer os.RemoveAll(workdir)

	m.log.Info("checking out", "sha", sha[:12], "dir", workdir)
	cmd := exec.CommandContext(ctx, "git", "clone", "--shared", "--no-checkout", m.repoDir, workdir)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", "", fmt.Errorf("git clone --shared: %w\n%s", err, out)
	}
	cmd = exec.CommandContext(ctx, "git", "-C", workdir, "checkout", "--detach", sha)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", "", fmt.Errorf("git checkout: %w\n%s", err, out)
	}

	// If recipe specifies a subdirectory, use it as the build root.
	buildRoot := workdir
	if recipe.BuildDir != "" {
		buildRoot = filepath.Join(workdir, recipe.BuildDir)
	}

	// Run pre-build commands (e.g. building embedded assets).
	for _, cmd := range recipe.PreBuildCmds {
		m.log.Info("pre-build", "process", process, "cmd", cmd)
		pre := exec.CommandContext(ctx, "bash", "-c", cmd)
		pre.Dir = buildRoot
		pre.Env = append(os.Environ(), "GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0")
		if out, err := pre.CombinedOutput(); err != nil {
			return "", "", fmt.Errorf("pre-build %q: %w\n%s", cmd, err, out)
		}
	}

	// Build the binary.
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		return "", "", fmt.Errorf("mkdir cache: %w", err)
	}

	m.log.Info("building", "process", process, "sha", sha[:12], "target", recipe.BuildTarget)
	buildStart := time.Now()
	buildCmd := exec.CommandContext(ctx, "go", "build", "-o", outputPath, recipe.BuildTarget)
	buildCmd.Dir = buildRoot
	buildCmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0")
	if out, err := buildCmd.CombinedOutput(); err != nil {
		return "", "", fmt.Errorf("go build: %w\n%s", err, out)
	}
	buildDur := time.Since(buildStart)

	info, err := os.Stat(outputPath)
	if err != nil {
		return "", "", fmt.Errorf("stat artifact: %w", err)
	}

	summary := fmt.Sprintf("built %s in %s (%s)", recipe.BuildTarget, buildDur.Round(time.Millisecond), formatBytes(info.Size()))
	m.log.Info("build complete", "process", process, "sha", sha[:12], "size", info.Size(), "duration", buildDur.Round(time.Millisecond))
	return outputPath, summary, nil
}

func (m *Manager) upload(ctx context.Context, d *deploy, recipe Recipe, localPath string) (string, error) {
	info, err := os.Stat(localPath)
	if err != nil {
		return "", fmt.Errorf("stat local: %w", err)
	}
	size := info.Size()

	ts := time.Now().Format("20060102-150405")
	remotePath := fmt.Sprintf("%s/%s.%s-%s", recipe.RemoteDir, recipe.BinaryName, ts, d.sha[:12])
	tmpName := fmt.Sprintf("deploy-%s-%s", recipe.BinaryName, d.sha[:12])
	tmpPath := "/tmp/" + tmpName

	// SCP to /tmp first (avoids permission issues), then sudo mv into place.
	user := recipe.remoteUser()
	scpStart := time.Now()
	scp := exec.CommandContext(ctx, "scp",
		"-o", "StrictHostKeyChecking=no",
		localPath, user+"@"+d.dnsName+":"+tmpPath)
	if out, err := scp.CombinedOutput(); err != nil {
		return "", fmt.Errorf("scp: %w\n%s", err, out)
	}
	scpDur := time.Since(scpStart)

	mbps := float64(size) / 1024 / 1024 / scpDur.Seconds()
	d.setStepOutput(fmt.Sprintf("%s in %s (%.1f MB/s) → %s", formatBytes(size), scpDur.Round(time.Millisecond), mbps, remotePath))

	if err := m.ssh(ctx, user, d.dnsName, "sudo", "mv", tmpPath, remotePath); err != nil {
		return "", fmt.Errorf("mv: %w", err)
	}
	if err := m.ssh(ctx, user, d.dnsName, "sudo", "chmod", "+x", remotePath); err != nil {
		return "", fmt.Errorf("chmod: %w", err)
	}

	return remotePath, nil
}

func (m *Manager) install(ctx context.Context, d *deploy, recipe Recipe, remotePath string) error {
	symlink := recipe.RemoteDir + "/" + recipe.BinaryName
	d.setStepOutput(fmt.Sprintf("%s → %s", symlink, remotePath))
	return m.ssh(ctx, recipe.remoteUser(), d.dnsName, "ln", "-sf", remotePath, symlink)
}

func (m *Manager) restart(ctx context.Context, d *deploy, recipe Recipe) error {
	d.setStepOutput(recipe.ServiceUnit)
	return m.ssh(ctx, recipe.remoteUser(), d.dnsName, "sudo", "systemctl", "restart", recipe.ServiceUnit)
}

func (m *Manager) verify(ctx context.Context, d *deploy, recipe Recipe) error {
	scheme := "http"
	if recipe.HealthTLS {
		scheme = "https"
	}
	url := fmt.Sprintf("%s://%s:%d%s", scheme, d.dnsName, recipe.HealthPort, recipe.HealthPath)

	start := time.Now()
	deadline := start.Add(30 * time.Second)
	attempts := 0
	var lastErr error
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return err
		}
		attempts++
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return fmt.Errorf("create request: %w", err)
		}
		resp, err := m.client.Do(req)
		if err != nil {
			lastErr = err
			time.Sleep(2 * time.Second)
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		resp.Body.Close()

		got := strings.TrimSpace(string(body))
		if got == d.sha {
			d.setStepOutput(fmt.Sprintf("SHA confirmed after %s (%d attempts)", time.Since(start).Round(time.Millisecond), attempts))
			return nil
		}
		lastErr = fmt.Errorf("want SHA %s, got %q", d.sha[:12], got)
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("health check timed out after %d attempts: %v", attempts, lastErr)
}

func (m *Manager) ssh(ctx context.Context, user, host string, args ...string) error {
	sshArgs := append([]string{"-o", "StrictHostKeyChecking=no", user + "@" + host}, args...)
	cmd := exec.CommandContext(ctx, "ssh", sshArgs...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ssh %s: %w\n%s", strings.Join(args, " "), err, out)
	}
	return nil
}

func generateID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func formatBytes(b int64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(b)/(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(b)/(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(b)/(1<<10))
	default:
		return fmt.Sprintf("%d B", b)
	}
}
