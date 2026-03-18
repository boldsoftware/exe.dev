package collector

import (
	"context"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// component defines how to find an exe component.
type component struct {
	serviceName string // systemctl service name (e.g. exelet, exeprox)
	processName string // binary name to look for in /proc (e.g. exeletd, exeprox)
	binaryName  string // binary name on disk (e.g. exeletd.latest, exeprox.latest)
	versionURL  string // if set, fetch version via HTTP GET instead of --version
}

var components = []component{
	{serviceName: "exelet", processName: "exeletd", binaryName: "exeletd.latest"},
	{serviceName: "exeprox", processName: "exeprox", binaryName: "exeprox.latest", versionURL: "http://localhost:8080/debug/gitsha"},
}

// Exe collects exelet and exeprox version and status.
type Exe struct {
	Components []ComponentInfo
}

func NewExe() *Exe { return &Exe{} }

func (e *Exe) Name() string { return "exe" }

func (e *Exe) Collect(ctx context.Context) error {
	e.Components = nil
	for _, comp := range components {
		c := e.collectComponent(ctx, comp)
		if c != nil {
			e.Components = append(e.Components, *c)
		}
	}
	return nil
}

func (e *Exe) collectComponent(ctx context.Context, comp component) *ComponentInfo {
	binPath := findBinary(comp)
	if binPath == "" {
		return nil
	}

	ci := &ComponentInfo{Name: comp.serviceName, Version: "unknown", Status: "unknown"}

	// Get version.
	if comp.versionURL != "" {
		ci.Version = fetchVersion(ctx, comp.versionURL)
	} else {
		cmd := exec.CommandContext(ctx, binPath, "--version")
		out, err := cmd.Output()
		if err == nil {
			ver := strings.TrimSpace(string(out))
			// Strip "<name> version " prefix if present (e.g. "exelet version f47c561" → "f47c561").
			ver = strings.TrimPrefix(ver, comp.serviceName+" version ")
			ver = strings.TrimPrefix(ver, comp.processName+" version ")
			ci.Version = ver
		}
	}

	// Get status via systemctl.
	statusCmd := exec.CommandContext(ctx, "systemctl", "is-active", comp.serviceName)
	statusOut, err := statusCmd.Output()
	if err == nil {
		ci.Status = strings.TrimSpace(string(statusOut))
	} else {
		ci.Status = "inactive"
	}

	return ci
}

// fetchVersion retrieves a version string from an HTTP endpoint.
func fetchVersion(ctx context.Context, url string) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "unknown"
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "unknown"
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 256))
	if err != nil || resp.StatusCode != http.StatusOK {
		return "unknown"
	}
	if v := strings.TrimSpace(string(body)); v != "" {
		if len(v) > 7 {
			v = v[:7]
		}
		return v
	}
	return "unknown"
}

// findBinary resolves the binary path using the following lookup order:
//  1. $HOME/<binaryName>  (e.g. ~/exeletd.latest)
//  2. exec.LookPath       (standard PATH search)
//  3. Process table        (scan /proc for running process by processName)
func findBinary(comp component) string {
	// 1. $HOME/<binaryName>
	if home, err := os.UserHomeDir(); err == nil {
		p := filepath.Join(home, comp.binaryName)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p
		}
	}

	// 2. LookPath — try binaryName (e.g. exeletd.latest), then processName (e.g. exeletd).
	if p, err := exec.LookPath(comp.binaryName); err == nil {
		return p
	}
	if p, err := exec.LookPath(comp.processName); err == nil {
		return p
	}

	// 3. Process table — match any binary starting with processName.
	if p := findProcessBinary(comp.processName); p != "" {
		return p
	}

	return ""
}

// findProcessBinary scans /proc to find a running process whose binary name
// starts with the given prefix (e.g. "exeletd" matches "exeletd" and
// "exeletd.latest"). Returns the resolved exe path, or "" if not found.
func findProcessBinary(name string) string {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return ""
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		// Skip non-numeric entries (not PIDs).
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}

		exePath := filepath.Join("/proc", entry.Name(), "exe")
		target, err := os.Readlink(exePath)
		if err != nil {
			continue // Permission denied or no longer exists.
		}

		base := filepath.Base(target)
		if base == name || strings.HasPrefix(base, name+".") {
			return target
		}
	}

	return ""
}
