package inventory

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Process represents a single process running on a fleet host.
type Process struct {
	Hostname       string  `json:"hostname"`
	DNSName        string  `json:"dns_name"`
	Role           string  `json:"role"`    // exelet, exeprox, exed
	Stage          string  `json:"stage"`   // prod, staging
	Region         string  `json:"region"`  // fra2, lax, nyc, etc.
	Process        string  `json:"process"` // exeletd, cgtop, exed, exeprox
	DebugURL       string  `json:"debug_url"`
	Version        string  `json:"version"`                   // git SHA or ""
	VersionSubject string  `json:"version_subject,omitempty"` // commit subject from git repo
	VersionDate    string  `json:"version_date,omitempty"`    // commit date (RFC 3339)
	VersionURL     string  `json:"version_url,omitempty"`     // github commit URL
	CommitsBehind  int     `json:"commits_behind"`            // -1 means unknown
	UptimeSecs     float64 `json:"uptime_secs"`               // 0 means unknown
}

// Inventory polls tailscale status and maintains an in-memory process list.
type Inventory struct {
	mu      sync.RWMutex
	procs   []Process
	log     *slog.Logger
	gitRepo *GitRepo
	client  *http.Client
}

// New creates a new Inventory service.
func New(log *slog.Logger, gitRepo *GitRepo) *Inventory {
	return &Inventory{
		log:     log,
		gitRepo: gitRepo,
		client: &http.Client{
			Timeout: 5 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{
					// Tailscale certs are self-signed from the perspective of
					// the system trust store; we trust the tailnet.
					InsecureSkipVerify: true,
				},
			},
		},
	}
}

// Run starts the inventory polling loop. It blocks until ctx is cancelled.
func (inv *Inventory) Run(ctx context.Context) {
	inv.refresh(ctx)

	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			inv.refresh(ctx)
		}
	}
}

// Processes returns a copy of the current process list.
func (inv *Inventory) Processes() []Process {
	inv.mu.RLock()
	defer inv.mu.RUnlock()
	out := make([]Process, len(inv.procs))
	copy(out, inv.procs)
	return out
}

// HeadSHA returns the current SHA of refs/heads/main from the git repo.
func (inv *Inventory) HeadSHA() string {
	if inv.gitRepo == nil {
		return ""
	}
	return inv.gitRepo.HeadSHA()
}

// HeadCommit returns the SHA, subject, and date of refs/heads/main.
func (inv *Inventory) HeadCommit() (sha, subject, date string) {
	if inv.gitRepo == nil {
		return "", "", ""
	}
	sha = inv.gitRepo.HeadSHA()
	if sha == "" {
		return "", "", ""
	}
	info, err := inv.gitRepo.ResolveCommit(sha)
	if err != nil {
		return sha, "", ""
	}
	if !info.Date.IsZero() {
		date = info.Date.Format(time.RFC3339)
	}
	return sha, info.Subject, date
}

// CommitLog returns commits from (fromSHA, toSHA], up to maxN.
func (inv *Inventory) CommitLog(fromSHA, toSHA string, maxN int) ([]LogEntry, error) {
	if inv.gitRepo == nil {
		return nil, nil
	}
	return inv.gitRepo.CommitLog(fromSHA, toSHA, maxN)
}

// tailscaleStatus is the subset of `tailscale status --json` we care about.
type tailscaleStatus struct {
	Self *tailscalePeer           `json:"Self"`
	Peer map[string]tailscalePeer `json:"Peer"`
}

type tailscalePeer struct {
	HostName string    `json:"HostName"`
	DNSName  string    `json:"DNSName"`
	Online   bool      `json:"Online"`
	LastSeen time.Time `json:"LastSeen"`
}

// Hostname patterns.
var (
	reExelet      = regexp.MustCompile(`^exelet-([a-z0-9]+)-([a-z]+)-\d+$`)
	reExeCtr      = regexp.MustCompile(`^exe-ctr-(\d+)$`)
	reExeprox     = regexp.MustCompile(`^exeprox-([a-z0-9]+)-([a-z]+)-\d+$`)
	reExeproxNA1  = regexp.MustCompile(`^exeprox-na-([a-z0-9]+)-\d+$`)
	reExeproxNA2  = regexp.MustCompile(`^exeprox-([a-z0-9]+)-na-\d+$`)
	reExed        = regexp.MustCompile(`^exed-\d+$`)
	reExedStaging = regexp.MustCompile(`^exed-staging-\d+$`)
	reExeOps      = regexp.MustCompile(`^exe-ops$`)
)

func classifyHost(hostname string) (role, stage, region string, ok bool) {
	if strings.HasSuffix(hostname, "-replica") {
		return "", "", "", false
	}
	if m := reExelet.FindStringSubmatch(hostname); m != nil {
		return "exelet", m[2], m[1], true
	}
	if reExeCtr.MatchString(hostname) {
		return "exelet", "prod", "", true
	}
	if m := reExeproxNA1.FindStringSubmatch(hostname); m != nil {
		return "exeprox", "prod", m[1], true
	}
	if m := reExeproxNA2.FindStringSubmatch(hostname); m != nil {
		return "exeprox", "prod", m[1], true
	}
	if m := reExeprox.FindStringSubmatch(hostname); m != nil {
		return "exeprox", m[2], m[1], true
	}
	if reExedStaging.MatchString(hostname) {
		return "exed", "staging", "", true
	}
	if reExed.MatchString(hostname) {
		return "exed", "prod", "", true
	}
	if reExeOps.MatchString(hostname) {
		return "exe-ops", "global", "", true
	}
	return "", "", "", false
}

// processSpec defines a process to discover on a host role.
type processSpec struct {
	name       string
	debugURL   func(dnsName string) string
	versionURL func(dnsName string) string // nil means no version endpoint
	metricsURL func(dnsName string) string // nil means no metrics endpoint
}

var processesByRole = map[string][]processSpec{
	"exelet": {
		{
			name:       "exeletd",
			debugURL:   func(d string) string { return "http://" + d + ":9081/debug/" },
			versionURL: func(d string) string { return "http://" + d + ":9081/debug/gitsha" },
			metricsURL: func(d string) string { return "http://" + d + ":9081/metrics" },
		},
		{
			name:       "cgtop",
			debugURL:   func(d string) string { return "http://" + d + ":9090/" },
			versionURL: func(d string) string { return "http://" + d + ":9090/debug/gitsha" },
			metricsURL: func(d string) string { return "http://" + d + ":9090/debug/metrics" },
		},
	},
	"exeprox": {
		{
			name:       "exeprox",
			debugURL:   func(d string) string { return "https://" + d + "/debug/" },
			versionURL: func(d string) string { return "https://" + d + "/debug/gitsha" },
			metricsURL: func(d string) string { return "https://" + d + "/metrics" },
		},
	},
	"exed": {
		{
			name:       "exed",
			debugURL:   func(d string) string { return "https://" + d + "/debug/" },
			versionURL: func(d string) string { return "https://" + d + "/debug/gitsha" },
			metricsURL: func(d string) string { return "https://" + d + "/metrics" },
		},
		{
			name:     "metricsd",
			debugURL: func(d string) string { return "http://" + d + ":21090/debug/pprof/" },
		},
	},
	"exe-ops": {
		{
			name:       "exe-ops",
			debugURL:   func(d string) string { return "https://" + d + "/" },
			versionURL: func(d string) string { return "https://" + d + "/debug/gitsha" },
		},
	},
}

func (inv *Inventory) refresh(ctx context.Context) {
	peers, err := inv.getTailscalePeers(ctx)
	if err != nil {
		inv.log.Error("tailscale status failed", "error", err)
		return
	}

	staleThreshold := time.Now().Add(-7 * 24 * time.Hour)

	var procs []Process
	for _, p := range peers {
		if !p.Online && !p.LastSeen.IsZero() && p.LastSeen.Before(staleThreshold) {
			continue
		}
		role, stage, region, ok := classifyHost(p.HostName)
		if !ok {
			continue
		}
		dnsName := strings.TrimSuffix(p.DNSName, ".")
		specs := processesByRole[role]
		for _, spec := range specs {
			procs = append(procs, Process{
				Hostname:      p.HostName,
				DNSName:       dnsName,
				Role:          role,
				Stage:         stage,
				Region:        region,
				Process:       spec.name,
				DebugURL:      spec.debugURL(dnsName),
				CommitsBehind: -1,
			})
		}
	}

	inv.fetchDetails(ctx, procs)

	inv.mu.Lock()
	inv.procs = procs
	inv.mu.Unlock()

	inv.log.Info("inventory refreshed", "processes", len(procs))
}

func (inv *Inventory) getTailscalePeers(ctx context.Context) ([]tailscalePeer, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	out, err := execCommand(ctx, "tailscale", "status", "--json")
	if err != nil {
		return nil, fmt.Errorf("tailscale status: %w", err)
	}

	var status tailscaleStatus
	if err := json.Unmarshal(out, &status); err != nil {
		return nil, fmt.Errorf("parse tailscale status: %w", err)
	}

	peers := make([]tailscalePeer, 0, len(status.Peer)+1)
	if status.Self != nil {
		self := *status.Self
		self.Online = true // self is always online
		peers = append(peers, self)
	}
	for _, p := range status.Peer {
		peers = append(peers, p)
	}
	return peers, nil
}

// fetchDetails fetches version and uptime for all processes concurrently.
func (inv *Inventory) fetchDetails(ctx context.Context, procs []Process) {
	sem := make(chan struct{}, 20)
	var wg sync.WaitGroup
	now := time.Now()

	for i := range procs {
		spec := findSpec(procs[i].Role, procs[i].Process)
		if spec == nil {
			continue
		}

		hasVersion := spec.versionURL != nil
		hasMetrics := spec.metricsURL != nil
		if !hasVersion && !hasMetrics {
			continue
		}

		wg.Add(1)
		go func(p *Process, spec *processSpec) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			// Fetch version.
			if spec.versionURL != nil {
				inv.fetchVersion(ctx, p, spec.versionURL(p.DNSName))
			}

			// Fetch uptime from metrics.
			if spec.metricsURL != nil {
				inv.fetchUptime(ctx, p, spec.metricsURL(p.DNSName), now)
			}
		}(&procs[i], spec)
	}
	wg.Wait()
}

func (inv *Inventory) fetchVersion(ctx context.Context, p *Process, url string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return
	}
	resp, err := inv.client.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 256))
	if err != nil {
		return
	}
	sha := strings.TrimSpace(string(body))
	if len(sha) == 40 && isHex(sha) {
		p.Version = sha
		p.VersionURL = "https://github.com/boldsoftware/exe/commit/" + sha
		p.CommitsBehind = -1

		if inv.gitRepo != nil {
			info, err := inv.gitRepo.ResolveCommit(sha)
			if err != nil {
				inv.log.Debug("resolve commit failed", "sha", sha, "error", err)
			} else {
				p.VersionSubject = info.Subject
				p.CommitsBehind = info.CommitsBehind
				if !info.Date.IsZero() {
					p.VersionDate = info.Date.Format(time.RFC3339)
				}
			}
		}
	}
}

func (inv *Inventory) fetchUptime(ctx context.Context, p *Process, url string, now time.Time) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return
	}
	resp, err := inv.client.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()

	// Scan for process_start_time_seconds line.
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "process_start_time_seconds ") {
			continue
		}
		valStr := strings.TrimPrefix(line, "process_start_time_seconds ")
		startTime, err := strconv.ParseFloat(valStr, 64)
		if err != nil {
			return
		}
		uptime := now.Sub(time.Unix(int64(startTime), int64(math.Mod(startTime, 1)*1e9)))
		if uptime > 0 {
			p.UptimeSecs = uptime.Seconds()
		}
		return
	}
}

func findSpec(role, process string) *processSpec {
	for i := range processesByRole[role] {
		if processesByRole[role][i].name == process {
			return &processesByRole[role][i]
		}
	}
	return nil
}

func isHex(s string) bool {
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}
