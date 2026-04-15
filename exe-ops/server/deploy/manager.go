package deploy

import (
	"bytes"
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
	"sync/atomic"
	"time"
)

const maxHistory = 100

// Manager orchestrates deploys. It enforces that only one deploy is
// active per (stage, role, process, host), and that only one build
// runs at a time per (process, sha).
type Manager struct {
	ctx context.Context // server lifetime

	mu             sync.Mutex
	deploys        []*deploy              // all deploys, most recent first
	active         map[string]*deploy     // key: activeKey()
	builds         map[string]*sync.Mutex // key: process/sha
	rollouts       []*rollout             // all rollouts, most recent first
	activeRollouts map[string]*rollout    // key: rolloutLockKey (process)

	repoDir  string // bare git clone (shared with inventory)
	cacheDir string // artifact cache root
	log      *slog.Logger
	client   *http.Client
	notifier Notifier // optional; nil = no notifications

	// onDeploy is called after every deploy finishes (success or failure).
	// Used to trigger an inventory refresh so the UI sees updated versions.
	onDeploy func()

	// runDeploy is the deploy execution function. Defaults to (*Manager).execute.
	// Tests override it to avoid invoking ssh/scp/git.
	runDeploy func(ctx context.Context, d *deploy)

	// prodLockAcquire takes the prod-lock for the given deploy stage with a
	// human-readable reason. It returns a release function (may be nil when
	// no lock was needed) and an error — a *ProdLockError if the env is
	// already locked or the check could not be completed. Defaults to the
	// real HTTP acquirer; tests override it to avoid contacting the server.
	prodLockAcquire func(ctx context.Context, stage, reason string) (release func(), err error)
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
	m := &Manager{
		ctx:            ctx,
		active:         make(map[string]*deploy),
		builds:         make(map[string]*sync.Mutex),
		activeRollouts: make(map[string]*rollout),
		repoDir:        repoDir,
		cacheDir:       cacheDir,
		log:            log,
		client: &http.Client{
			Timeout: 10 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			},
		},
	}
	m.prodLockAcquire = m.defaultProdLockAcquire
	return m
}

// SetNotifier configures an optional deploy lifecycle notifier (e.g. Slack).
func (m *Manager) SetNotifier(n Notifier) {
	m.notifier = n
}

// OnDeploy registers a callback that fires after every deploy finishes.
func (m *Manager) OnDeploy(f func()) {
	m.onDeploy = f
}

// Request describes a deploy to start.
type Request struct {
	Stage   string `json:"stage"`
	Role    string `json:"role"`
	Process string `json:"process"`
	Host    string `json:"host"`     // display hostname
	DNSName string `json:"dns_name"` // tailscale DNS for SSH
	SHA     string `json:"sha"`      // 40-char hex

	InitiatedBy string `json:"-"` // set by handler via Tailscale whois
}

// Start begins a new deploy. Returns an error if a deploy is already
// active for the same target or the process type is unknown.
func (m *Manager) Start(req Request) (Status, error) {
	if err := m.validateRequest(req); err != nil {
		return Status{}, err
	}
	var release func()
	if prodLockStage(req.Stage) != "" {
		r, err := m.prodLockAcquire(m.ctx, req.Stage, prodLockReasonDeploy(req))
		if err != nil {
			return Status{}, err
		}
		release = r
	}
	status, err := m.start(req, "", release)
	if err != nil && release != nil {
		release()
	}
	return status, err
}

// validateRequest applies the same validation as Start without spawning anything.
// Used by StartRollout to fail fast on a malformed batch.
func (m *Manager) validateRequest(req Request) error {
	if req.Stage != "staging" && req.Stage != "global" && req.Stage != "prod" {
		return fmt.Errorf("only staging, prod, and global deploys are currently allowed")
	}
	if req.Stage == "prod" && !prodDeployAllowed(req.Process) {
		return fmt.Errorf("prod deploys not allowed for %q", req.Process)
	}
	if _, ok := Recipes[req.Process]; !ok {
		return fmt.Errorf("unknown process %q", req.Process)
	}
	if len(req.SHA) != 40 {
		return fmt.Errorf("sha must be 40 hex characters")
	}
	return nil
}

func (m *Manager) start(req Request, rolloutID string, prodLockRelease func()) (Status, error) {
	if err := m.validateRequest(req); err != nil {
		return Status{}, err
	}

	id := generateID()
	d := newDeploy(m.ctx, id, req.Stage, req.Role, req.Process, req.Host, req.DNSName, req.SHA, req.InitiatedBy, rolloutID)
	d.releaseProdLock = prodLockRelease

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
		"host", req.Host, "sha", req.SHA[:12], "rollout_id", rolloutID)

	if m.notifier != nil {
		go m.notifier.DeployStarted(d.snapshot())
	}

	runner := m.runDeploy
	if runner == nil {
		runner = m.execute
	}
	go func() {
		defer m.finish(d)
		runner(d.ctx, d)
	}()
	return d.snapshot(), nil
}

// getDeployByID returns the internal *deploy with the given id, or nil.
func (m *Manager) getDeployByID(id string) *deploy {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, d := range m.deploys {
		if d.id == id {
			return d
		}
	}
	return nil
}

// List returns snapshots of deploys started at or after since, most recent first.
// Active (non-terminal) deploys are always included regardless of since.
func (m *Manager) List(since time.Time) []Status {
	m.mu.Lock()
	deploys := make([]*deploy, len(m.deploys))
	copy(deploys, m.deploys)
	m.mu.Unlock()

	out := make([]Status, 0, len(deploys))
	for _, d := range deploys {
		s := d.snapshot()
		if s.StartedAt.Before(since) && (s.State == "done" || s.State == "failed" || s.State == "cancelled") {
			continue
		}
		out = append(out, s)
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

// ErrDeployRolloutOwned is returned by Cancel when the caller tries to
// cancel a deploy that belongs to a rollout. Rollout-owned deploys must
// be cancelled via CancelRollout so that the rollout orchestrator stops
// scheduling further waves.
var ErrDeployRolloutOwned = fmt.Errorf("deploy is part of a rollout; cancel the rollout instead")

// Cancel aborts an in-flight deploy by id. Idempotent: cancelling a deploy
// that has already reached a terminal state is a no-op and returns nil.
// Rollout-owned deploys cannot be cancelled individually; the caller must
// cancel the rollout instead — in that case ErrDeployRolloutOwned is
// returned.
func (m *Manager) Cancel(id string) error {
	m.mu.Lock()
	var found *deploy
	for _, d := range m.deploys {
		if d.id == id {
			found = d
			break
		}
	}
	m.mu.Unlock()
	if found == nil {
		return fmt.Errorf("deploy %s not found", id)
	}
	if found.rolloutID != "" {
		return ErrDeployRolloutOwned
	}
	found.requestCancel()
	return nil
}

// execute runs the full deploy pipeline as a goroutine.
// The caller is responsible for calling m.finish(d) afterwards.
func (m *Manager) execute(ctx context.Context, d *deploy) {
	recipe := Recipes[d.process]

	// Build (checkout + compile, cached and deduplicated).
	d.beginStep("build")
	artifact, buildOutput, err := m.ensureArtifact(ctx, d, d.process, d.sha, recipe)
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

	// Install the systemd service file from the repo at this SHA.
	if len(recipe.ServiceFiles) > 0 {
		d.beginStep("service")
		err = m.installServiceFile(ctx, d, recipe)
		if d.stepDone(err) {
			return
		}
	}

	// Run pre-restart commands (e.g. database backup).
	if len(recipe.PreRestartCmds) > 0 {
		d.beginStep("backup")
		err = m.preRestart(ctx, d, recipe)
		if d.stepDone(err) {
			return
		}
	}

	// Run preflight checks (e.g. migration validation).
	if len(recipe.PreflightCmds) > 0 {
		d.beginStep("preflight")
		err = m.preflight(ctx, d, recipe, remotePath)
		if d.stepDone(err) {
			return
		}
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

	// Release the deploy's context. Safe to call even if already cancelled
	// by a rollout cancel; this just frees the context resources.
	if d.cancel != nil {
		d.cancel()
	}

	// Release the prod-lock for single-host deploys. Rollout-driven deploys
	// leave this nil because the rollout holds (and releases) the lock at
	// its own lifetime boundary.
	if d.releaseProdLock != nil {
		d.releaseProdLock()
	}

	// Signal any waiters (e.g. rollout orchestrator) that this deploy
	// has reached a terminal state. Close once: state is guarded by
	// reaching finish, which only runs from execute()'s defer.
	close(d.done)

	if m.notifier != nil {
		m.notifier.DeployFinished(d.snapshot())
	}
	if m.onDeploy != nil {
		m.onDeploy()
	}
}

// ensureArtifact returns the path to a built binary and a human summary,
// using the cache and serializing concurrent builds for the same (process, sha).
func (m *Manager) ensureArtifact(ctx context.Context, d *deploy, process, sha string, recipe Recipe) (string, string, error) {
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

	return m.buildArtifact(ctx, d, process, sha, recipe, artifactPath)
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

// buildEnv returns os.Environ() with GOOS/GOARCH/CGO_ENABLED for compilation,
// plus sensible defaults for PATH/GOPATH/HOME so builds work under systemd.
// When cgo is true, CGO_ENABLED=1 is set (for packages that need C linkage).
func buildEnv(cgo bool) []string {
	env := os.Environ()
	has := map[string]bool{}
	for _, e := range env {
		if i := strings.IndexByte(e, '='); i > 0 {
			has[e[:i]] = true
		}
	}
	if !has["HOME"] {
		env = append(env, "HOME=/root")
	}
	if !has["GOPATH"] {
		home := "/root"
		for _, e := range env {
			if strings.HasPrefix(e, "HOME=") {
				home = e[5:]
				break
			}
		}
		env = append(env, "GOPATH="+home+"/go")
	}
	if !has["PATH"] {
		env = append(env, "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin")
	} else {
		// Ensure common dirs are present even if systemd set a minimal PATH.
		for i, e := range env {
			if strings.HasPrefix(e, "PATH=") {
				p := e[5:]
				for _, d := range []string{"/usr/local/bin", "/usr/bin", "/usr/local/sbin"} {
					if !strings.Contains(p, d) {
						p = p + ":" + d
					}
				}
				env[i] = "PATH=" + p
				break
			}
		}
	}
	cgoVal := "0"
	if cgo {
		cgoVal = "1"
	}
	return append(env, "GOOS=linux", "GOARCH=amd64", "CGO_ENABLED="+cgoVal)
}

// liveWriter is an io.Writer that tracks the last non-empty line of output
// and forwards it to a deploy step via setStepOutput.
type liveWriter struct {
	d      *deploy
	prefix string // prepended to output, e.g. "building: "
	buf    []byte
	all    bytes.Buffer // full output for error reporting
}

func (w *liveWriter) Write(p []byte) (int, error) {
	w.all.Write(p)
	w.buf = append(w.buf, p...)
	// Find the last complete line and update the step output.
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		line := strings.TrimSpace(string(w.buf[:i]))
		w.buf = w.buf[i+1:]
		if line != "" {
			w.d.setStepOutput(w.prefix + line)
		}
	}
	return len(p), nil
}

// runCmd runs a command, streaming its output to the deploy step as live status.
// Returns the full combined output and any error.
func (m *Manager) runCmd(cmd *exec.Cmd, d *deploy, prefix string) (string, error) {
	w := &liveWriter{d: d, prefix: prefix}
	cmd.Stdout = w
	cmd.Stderr = w
	err := cmd.Run()
	return w.all.String(), err
}

func (m *Manager) buildArtifact(ctx context.Context, d *deploy, process, sha string, recipe Recipe, outputPath string) (string, string, error) {
	// Clone from the bare repo with --shared (uses hardlinks, fast)
	// so Go's VCS detection embeds vcs.revision in the binary.
	workdir, err := os.MkdirTemp("", "deploy-"+process+"-"+sha[:12]+"-*")
	if err != nil {
		return "", "", fmt.Errorf("create workdir: %w", err)
	}
	defer os.RemoveAll(workdir)

	d.setStepOutput("checking out " + sha[:12])
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
		pre := exec.CommandContext(ctx, "bash", "-l", "-c", cmd)
		pre.Dir = buildRoot
		pre.Env = buildEnv(recipe.CGO)
		if out, err := m.runCmd(pre, d, "pre-build: "); err != nil {
			return "", "", fmt.Errorf("pre-build %q: %w\n%s", cmd, err, out)
		}
	}

	// Build the binary.
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		return "", "", fmt.Errorf("mkdir cache: %w", err)
	}

	m.log.Info("building", "process", process, "sha", sha[:12], "target", recipe.BuildTarget)
	buildStart := time.Now()
	buildCmd := exec.CommandContext(ctx, "go", "build", "-trimpath", "-v", "-o", outputPath, recipe.BuildTarget)
	buildCmd.Dir = buildRoot
	buildCmd.Env = buildEnv(recipe.CGO)
	if out, err := m.runCmd(buildCmd, d, "compiling: "); err != nil {
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

	user := recipe.remoteUser()

	// Stream the binary over ssh using `cat > tmpPath`. Piping through
	// stdin lets us wrap the reader and track bytes sent, so we can
	// publish live progress on the "upload" step — scp has no
	// machine-readable progress output.
	f, err := os.Open(localPath)
	if err != nil {
		return "", fmt.Errorf("open local: %w", err)
	}
	defer f.Close()

	pr := &progressReader{r: f}
	pr.total.Store(size)

	uploadStart := time.Now()
	// Background ticker publishes live progress to the step output.
	progressStop := make(chan struct{})
	progressDone := make(chan struct{})
	go func() {
		defer close(progressDone)
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-progressStop:
				return
			case <-ticker.C:
				sent := pr.sent.Load()
				elapsed := time.Since(uploadStart).Seconds()
				var mbps float64
				if elapsed > 0 {
					mbps = float64(sent) / 1024 / 1024 / elapsed
				}
				var pct int
				if size > 0 {
					pct = int(sent * 100 / size)
				}
				d.setStepOutput(fmt.Sprintf("%s / %s (%d%%, %.1f MB/s)",
					formatBytes(sent), formatBytes(size), pct, mbps))
			}
		}
	}()

	cmd := exec.CommandContext(ctx, "ssh",
		"-o", "StrictHostKeyChecking=no",
		user+"@"+d.dnsName, "cat > "+tmpPath)
	cmd.Stdin = pr
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	close(progressStop)
	<-progressDone
	if runErr != nil {
		return "", fmt.Errorf("upload: %w\n%s", runErr, stderr.String())
	}
	// Safety check: ensure we actually sent the full file. A partial
	// transfer with a clean exit on the remote side (unlikely but
	// possible) would otherwise pass a truncated binary forward.
	if got := pr.sent.Load(); got != size {
		return "", fmt.Errorf("upload short: sent %d of %d bytes", got, size)
	}

	uploadDur := time.Since(uploadStart)
	mbps := float64(size) / 1024 / 1024 / uploadDur.Seconds()
	d.setStepOutput(fmt.Sprintf("%s in %s (%.1f MB/s) → %s", formatBytes(size), uploadDur.Round(time.Millisecond), mbps, remotePath))

	if err := m.ssh(ctx, user, d.dnsName, "sudo", "mv", tmpPath, remotePath); err != nil {
		return "", fmt.Errorf("mv: %w", err)
	}
	if err := m.ssh(ctx, user, d.dnsName, "sudo", "chmod", "+x", remotePath); err != nil {
		return "", fmt.Errorf("chmod: %w", err)
	}

	return remotePath, nil
}

// progressReader wraps an io.Reader and atomically tracks bytes read.
// Used by upload() to publish live transfer progress.
type progressReader struct {
	r     io.Reader
	sent  atomic.Int64
	total atomic.Int64
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	if n > 0 {
		p.sent.Add(int64(n))
	}
	return n, err
}

func (m *Manager) install(ctx context.Context, d *deploy, recipe Recipe, remotePath string) error {
	name := recipe.symlinkName()
	if name == "-" {
		d.setStepOutput("skipped (no symlink)")
		return nil
	}
	symlink := recipe.RemoteDir + "/" + name
	d.setStepOutput(fmt.Sprintf("%s → %s", symlink, remotePath))
	return m.ssh(ctx, recipe.remoteUser(), d.dnsName, "sudo", "ln", "-sf", remotePath, symlink)
}

// installServiceFile extracts the service file from the bare repo at the
// deploy SHA, copies it to the remote host, and installs it into
// /etc/systemd/system/ with a daemon-reload.
func (m *Manager) installServiceFile(ctx context.Context, d *deploy, recipe Recipe) error {
	repoPath := recipe.serviceFile(d.stage)
	if repoPath == "" {
		d.setStepOutput("skipped (no service file for stage " + d.stage + ")")
		return nil
	}

	// Extract the file content from the bare repo at the deploy SHA.
	cmd := exec.CommandContext(ctx, "git", "-C", m.repoDir, "show", d.sha+":"+repoPath)
	content, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("git show %s:%s: %w", d.sha[:12], repoPath, err)
	}

	// Write to a temp file for scp.
	tmp, err := os.CreateTemp("", "service-*")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp: %w", err)
	}
	tmp.Close()

	user := recipe.remoteUser()
	remoteTmp := "/tmp/deploy-" + recipe.ServiceUnit

	// SCP the service file to /tmp on the remote host.
	scp := exec.CommandContext(ctx, "scp",
		"-o", "StrictHostKeyChecking=no",
		tmp.Name(), user+"@"+d.dnsName+":"+remoteTmp)
	if out, err := scp.CombinedOutput(); err != nil {
		return fmt.Errorf("scp service file: %w\n%s", err, out)
	}

	// Move into place and reload systemd.
	remoteDest := "/etc/systemd/system/" + recipe.ServiceUnit
	if err := m.ssh(ctx, user, d.dnsName, "sudo", "mv", remoteTmp, remoteDest); err != nil {
		return fmt.Errorf("mv service file: %w", err)
	}
	if err := m.ssh(ctx, user, d.dnsName, "sudo", "systemctl", "daemon-reload"); err != nil {
		return fmt.Errorf("daemon-reload: %w", err)
	}

	d.setStepOutput(fmt.Sprintf("%s → %s", repoPath, remoteDest))
	return nil
}

func (m *Manager) preRestart(ctx context.Context, d *deploy, recipe Recipe) error {
	user := recipe.remoteUser()
	for i, cmd := range recipe.PreRestartCmds {
		d.setStepOutput(fmt.Sprintf("running command %d/%d", i+1, len(recipe.PreRestartCmds)))
		if err := m.ssh(ctx, user, d.dnsName, cmd); err != nil {
			return fmt.Errorf("pre-restart cmd %d: %w", i+1, err)
		}
	}
	d.setStepOutput(fmt.Sprintf("%d command(s) completed", len(recipe.PreRestartCmds)))
	return nil
}

func (m *Manager) preflight(ctx context.Context, d *deploy, recipe Recipe, remotePath string) error {
	user := recipe.remoteUser()
	replacer := strings.NewReplacer("{binary}", remotePath, "{stage}", d.stage)
	for i, cmd := range recipe.PreflightCmds {
		expanded := replacer.Replace(cmd)
		d.setStepOutput(fmt.Sprintf("running check %d/%d", i+1, len(recipe.PreflightCmds)))
		if err := m.ssh(ctx, user, d.dnsName, expanded); err != nil {
			return fmt.Errorf("preflight cmd %d: %w", i+1, err)
		}
	}
	d.setStepOutput(fmt.Sprintf("%d check(s) passed", len(recipe.PreflightCmds)))
	return nil
}

func (m *Manager) restart(ctx context.Context, d *deploy, recipe Recipe) error {
	d.setStepOutput(recipe.ServiceUnit)
	if err := m.ssh(ctx, recipe.remoteUser(), d.dnsName, "sudo", "systemctl", "daemon-reload"); err != nil {
		return fmt.Errorf("daemon-reload: %w", err)
	}
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

// StartRollout begins a phased rollout. Returns 409-style "deployment in
// progress" if another rollout is already active for the same process.
func (m *Manager) StartRollout(req RolloutRequest) (RolloutStatus, error) {
	for i := range req.Targets {
		if req.Targets[i].Region == "" {
			req.Targets[i].Region = "default"
		}
	}
	if err := m.rolloutValidate(req); err != nil {
		return RolloutStatus{}, err
	}

	// Acquire the prod-lock once per distinct lock-gated stage in the
	// rollout. Rollouts normally share a single stage but the request
	// schema does not enforce this, so we lock each stage we see. On any
	// failure, release the locks we already took.
	var releases []func()
	releaseAll := func() {
		for _, rel := range releases {
			if rel != nil {
				rel()
			}
		}
	}
	lockedStage := map[string]int{}
	for _, t := range req.Targets {
		if prodLockStage(t.Stage) == "" {
			continue
		}
		lockedStage[t.Stage]++
	}
	for stage, count := range lockedStage {
		release, err := m.prodLockAcquire(m.ctx, stage, prodLockReasonRollout(req, stage, count))
		if err != nil {
			releaseAll()
			return RolloutStatus{}, err
		}
		if release != nil {
			releases = append(releases, release)
		}
	}

	waves := planWaves(req.Targets, effectiveBatchSize(req))
	r := newRollout(generateID(), req, waves)
	r.releaseProdLocks = releases

	lockKey := rolloutLockKey(req)

	m.mu.Lock()
	if existing, ok := m.activeRollouts[lockKey]; ok {
		m.mu.Unlock()
		releaseAll()
		return RolloutStatus{}, fmt.Errorf(
			"deployment in progress for %s (rollout %s, started %s by %s)",
			lockKey,
			existing.id,
			existing.startedAt.Format(time.RFC3339),
			existing.initiatedBy,
		)
	}
	m.activeRollouts[lockKey] = r
	m.rollouts = append([]*rollout{r}, m.rollouts...)
	if len(m.rollouts) > maxHistory {
		m.rollouts = m.rollouts[:maxHistory]
	}
	m.mu.Unlock()

	m.log.Info("rollout started",
		"id", r.id, "process", r.process, "sha", r.sha[:12],
		"targets", len(req.Targets), "waves", len(waves),
		"batch_size", r.batchSize, "cooldown", r.cooldown,
		"initiated_by", r.initiatedBy)

	go m.runRollout(r)
	return r.snapshot(), nil
}

// GetRollout returns a snapshot of a rollout by id.
func (m *Manager) GetRollout(id string) (RolloutStatus, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.rollouts {
		if r.id == id {
			return r.snapshot(), true
		}
	}
	return RolloutStatus{}, false
}

// ListRollouts returns snapshots of rollouts started at or after since,
// most recent first. Active (non-terminal) rollouts are always included.
func (m *Manager) ListRollouts(since time.Time) []RolloutStatus {
	m.mu.Lock()
	rs := make([]*rollout, len(m.rollouts))
	copy(rs, m.rollouts)
	m.mu.Unlock()

	out := make([]RolloutStatus, 0, len(rs))
	for _, r := range rs {
		s := r.snapshot()
		if s.StartedAt.Before(since) && terminalRollout(s.State) {
			continue
		}
		out = append(out, s)
	}
	return out
}

// CancelRollout marks a rollout for cancellation. Subsequent waves are
// skipped; in-flight deploys in the current wave run to completion.
func (m *Manager) CancelRollout(id string) error {
	r := m.findRollout(id)
	if r == nil {
		return fmt.Errorf("rollout %s not found", id)
	}
	r.requestCancel()
	return nil
}

// PauseRollout marks a rollout as pause-requested. The pause takes effect
// at the next wave boundary — if a wave is currently running, it finishes
// first; if the rollout is in cooldown, the cooldown timer is interrupted.
// Idempotent and safe to call on a rollout that has already reached a
// terminal state (returns nil in that case).
func (m *Manager) PauseRollout(id string) error {
	r := m.findRollout(id)
	if r == nil {
		return fmt.Errorf("rollout %s not found", id)
	}
	r.requestPause()
	return nil
}

// ResumeRollout clears a rollout's pause flag and unblocks the orchestrator
// so the next wave can start. Idempotent.
func (m *Manager) ResumeRollout(id string) error {
	r := m.findRollout(id)
	if r == nil {
		return fmt.Errorf("rollout %s not found", id)
	}
	r.requestResume()
	return nil
}

// findRollout returns the rollout with the given id, or nil. The returned
// pointer remains valid because rollouts are never removed from m.rollouts
// (history is bounded by maxHistory but trimmed from the tail).
func (m *Manager) findRollout(id string) *rollout {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.rollouts {
		if r.id == id {
			return r
		}
	}
	return nil
}

func terminalRollout(state string) bool {
	return state == "done" || state == "failed" || state == "cancelled"
}

// runRollout is the rollout orchestrator goroutine. It executes waves
// sequentially, waits between waves, and observes cancellation and
// stop-on-failure semantics.
func (m *Manager) runRollout(r *rollout) {
	defer m.finishRollout(r)

	r.mu.Lock()
	r.state = "running"
	r.mu.Unlock()

	for waveIdx, w := range r.waves {
		if r.cancelled() {
			m.markRemainingSkipped(r, waveIdx)
			r.mu.Lock()
			r.state = "cancelled"
			r.mu.Unlock()
			return
		}

		// Honor pause at the wave boundary. If the user clicked Pause during
		// the previous wave (or during cooldown), wait here until they
		// resume — or cancel.
		if ch := r.pauseGate(); ch != nil {
			r.mu.Lock()
			r.state = "paused"
			r.cooldownUntil = time.Time{}
			r.mu.Unlock()
			m.log.Info("rollout paused", "id", r.id, "wave", waveIdx)
			select {
			case <-ch:
				m.log.Info("rollout resumed", "id", r.id, "wave", waveIdx)
			case <-r.cancelCh:
				m.markRemainingSkipped(r, waveIdx)
				r.mu.Lock()
				r.state = "cancelled"
				r.mu.Unlock()
				return
			case <-m.ctx.Done():
				m.markRemainingSkipped(r, waveIdx)
				r.mu.Lock()
				r.state = "cancelled"
				r.err = "server shutting down"
				r.mu.Unlock()
				return
			}
		}

		r.mu.Lock()
		r.currentWave = waveIdx
		r.state = "running"
		w.state = "running"
		w.startedAt = time.Now()
		r.mu.Unlock()

		m.log.Info("rollout wave starting",
			"id", r.id, "wave", waveIdx, "region", w.region,
			"targets", len(w.requests))

		// Spawn all deploys in this wave. m.start is non-blocking (it
		// just validates, registers the deploy, and kicks off a goroutine)
		// so we can collect IDs synchronously before waiting. Publishing
		// the deploy ids to the wave immediately is what lets the UI
		// show per-step progress while the wave is running.
		waveFailed := false
		deployIDs := make([]string, len(w.requests))
		waveDeploys := make([]*deploy, len(w.requests))
		for i, req := range w.requests {
			st, err := m.start(req, r.id, nil)
			if err != nil {
				m.log.Error("rollout deploy failed to start", "id", r.id, "wave", waveIdx, "err", err)
				waveFailed = true
				r.mu.Lock()
				r.failed++
				r.mu.Unlock()
				continue
			}
			deployIDs[i] = st.ID
			waveDeploys[i] = m.getDeployByID(st.ID)
		}

		// Publish deploy ids to the wave state so the UI can see which
		// deploys are active and render per-step progress for them.
		r.mu.Lock()
		w.deployIDs = deployIDs
		r.mu.Unlock()

		// Watch for cancellation in parallel with waiting for deploys.
		// When the user cancels, we cancel every deploy's context so any
		// in-flight ssh/scp/http calls abort, and the wave can wind down
		// without waiting for slow uploads or hung verifies to complete.
		waveCancelStop := make(chan struct{})
		go func() {
			select {
			case <-r.cancelCh:
				for _, d := range waveDeploys {
					if d != nil {
						d.requestCancel()
					}
				}
			case <-waveCancelStop:
			}
		}()

		// Wait for each spawned deploy to reach a terminal state and
		// tally outcomes.
		for _, d := range waveDeploys {
			if d == nil {
				continue
			}
			select {
			case <-d.done:
			case <-m.ctx.Done():
				close(waveCancelStop)
				r.mu.Lock()
				r.state = "cancelled"
				r.err = "server shutting down"
				r.mu.Unlock()
				m.markRemainingSkipped(r, waveIdx+1)
				return
			}
			d.mu.Lock()
			state := d.state
			d.mu.Unlock()
			switch state {
			case "failed":
				waveFailed = true
				r.mu.Lock()
				r.failed++
				r.mu.Unlock()
			case "cancelled":
				// Cancelled deploys (from a rollout cancel) are not counted
				// as completed or failed — the rollout itself is about to
				// transition to "cancelled" via the r.cancelled() check below.
			default:
				r.mu.Lock()
				r.completed++
				r.mu.Unlock()
			}
		}
		close(waveCancelStop)

		// If the rollout was cancelled, the wave's deploys were aborted
		// mid-flight. Record the wave as cancelled rather than failed and
		// stop the rollout.
		if r.cancelled() {
			r.mu.Lock()
			w.doneAt = time.Now()
			w.state = "cancelled"
			r.mu.Unlock()
			m.markRemainingSkipped(r, waveIdx+1)
			r.mu.Lock()
			r.state = "cancelled"
			r.mu.Unlock()
			return
		}

		r.mu.Lock()
		w.doneAt = time.Now()
		if waveFailed {
			w.state = "failed"
		} else {
			w.state = "done"
		}
		r.mu.Unlock()

		if waveFailed && r.stopOnFailure {
			m.log.Warn("rollout aborting after wave failure",
				"id", r.id, "wave", waveIdx)
			m.markRemainingSkipped(r, waveIdx+1)
			r.mu.Lock()
			r.state = "failed"
			r.err = fmt.Sprintf("wave %d failed", waveIdx)
			r.mu.Unlock()
			return
		}

		// Cooldown before next wave (skip after the last wave).
		if waveIdx < len(r.waves)-1 {
			r.mu.Lock()
			r.state = "cooldown"
			r.cooldownUntil = time.Now().Add(r.cooldown)
			r.mu.Unlock()

			m.log.Info("rollout cooldown", "id", r.id, "duration", r.cooldown)
			t := time.NewTimer(r.cooldown)
			select {
			case <-t.C:
			case <-r.cancelCh:
				if !t.Stop() {
					<-t.C
				}
				m.markRemainingSkipped(r, waveIdx+1)
				r.mu.Lock()
				r.cooldownUntil = time.Time{}
				r.state = "cancelled"
				r.mu.Unlock()
				return
			case <-r.pauseSignalCh:
				// Pause requested mid-cooldown. Stop the timer and let the
				// next iteration's pause gate handle the wait.
				if !t.Stop() {
					select {
					case <-t.C:
					default:
					}
				}
				r.mu.Lock()
				r.cooldownUntil = time.Time{}
				r.mu.Unlock()
			case <-m.ctx.Done():
				if !t.Stop() {
					<-t.C
				}
				m.markRemainingSkipped(r, waveIdx+1)
				r.mu.Lock()
				r.cooldownUntil = time.Time{}
				r.state = "cancelled"
				r.err = "server shutting down"
				r.mu.Unlock()
				return
			}
			r.mu.Lock()
			r.cooldownUntil = time.Time{}
			r.mu.Unlock()
		}
	}

	r.mu.Lock()
	if r.state != "failed" && r.state != "cancelled" {
		if r.failed > 0 {
			r.state = "failed"
		} else {
			r.state = "done"
		}
	}
	r.mu.Unlock()
}

// markRemainingSkipped flips state on every wave at index >= from to "skipped".
func (m *Manager) markRemainingSkipped(r *rollout, from int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := from; i < len(r.waves); i++ {
		if r.waves[i].state == "pending" {
			r.waves[i].state = "skipped"
		}
	}
}

// finishRollout removes the rollout from the active map and stamps doneAt.
func (m *Manager) finishRollout(r *rollout) {
	r.mu.Lock()
	r.doneAt = time.Now()
	releases := r.releaseProdLocks
	r.releaseProdLocks = nil
	r.mu.Unlock()

	m.mu.Lock()
	if m.activeRollouts[r.process] == r {
		delete(m.activeRollouts, r.process)
	}
	m.mu.Unlock()

	for _, rel := range releases {
		if rel != nil {
			rel()
		}
	}

	m.log.Info("rollout finished",
		"id", r.id, "state", r.state,
		"completed", r.completed, "failed", r.failed)
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
