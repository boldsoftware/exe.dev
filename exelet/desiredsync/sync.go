// Package desiredsync polls exed for desired cgroup state and reconciles.
package desiredsync

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"exe.dev/desiredstate"
)

const (
	// DefaultPollInterval is how often the exelet polls exed for desired state.
	DefaultPollInterval = 1 * time.Minute
	// DefaultHTTPTimeout is the timeout for the HTTP request to exed.
	DefaultHTTPTimeout = 10 * time.Second
	// cgroupSlice matches the exelet resource manager's slice name.
	cgroupSlice = "exelet.slice"
)

// DeviceResolver resolves a VM ID to its block device's MAJ:MIN string.
type DeviceResolver interface {
	// ResolveDevice returns the "MAJ:MIN" string for a VM's block device.
	ResolveDevice(ctx context.Context, vmID string) (string, error)
}

// Syncer periodically fetches desired state from exed and reconciles cgroup settings.
type Syncer struct {
	log            *slog.Logger
	exedURL        string // base URL of exed (e.g., "http://exed-02:8080")
	exeletAddr     string // this exelet's address (e.g., "tcp://host:9080")
	cgroupRoot     string
	pollInterval   time.Duration
	httpClient     *http.Client
	deviceResolver DeviceResolver

	mu     sync.Mutex
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// Config holds configuration for the desired-state syncer.
type Config struct {
	ExedURL        string
	ExeletAddr     string
	CgroupRoot     string
	PollInterval   time.Duration
	DeviceResolver DeviceResolver
}

// New creates a new desired-state syncer.
func New(cfg Config, log *slog.Logger) (*Syncer, error) {
	if cfg.ExedURL == "" {
		return nil, fmt.Errorf("exed URL is required")
	}
	if cfg.ExeletAddr == "" {
		return nil, fmt.Errorf("exelet address is required")
	}

	cgroupRoot := cfg.CgroupRoot
	if cgroupRoot == "" {
		cgroupRoot = "/sys/fs/cgroup"
	}

	pollInterval := cfg.PollInterval
	if pollInterval == 0 {
		pollInterval = DefaultPollInterval
	}

	return &Syncer{
		log:            log,
		exedURL:        strings.TrimRight(cfg.ExedURL, "/"),
		exeletAddr:     cfg.ExeletAddr,
		cgroupRoot:     cgroupRoot,
		pollInterval:   pollInterval,
		deviceResolver: cfg.DeviceResolver,
		httpClient: &http.Client{
			Timeout: DefaultHTTPTimeout,
		},
	}, nil
}

// Start begins the periodic polling loop.
func (s *Syncer) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.cancel != nil {
		return nil
	}

	pollCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel

	s.wg.Go(func() {
		s.run(pollCtx)
	})

	s.log.InfoContext(ctx, "desired-state syncer started",
		"exed_url", s.exedURL,
		"exelet_addr", s.exeletAddr,
		"poll_interval", s.pollInterval)

	return nil
}

// Stop stops the syncer.
func (s *Syncer) Stop() {
	s.mu.Lock()
	cancel := s.cancel
	s.cancel = nil
	s.mu.Unlock()

	if cancel != nil {
		cancel()
		s.wg.Wait()
	}
}

// Refresh triggers an immediate poll outside the normal interval.
func (s *Syncer) Refresh(ctx context.Context) {
	s.poll(ctx)
}

func (s *Syncer) run(ctx context.Context) {
	ticker := time.NewTicker(s.pollInterval)
	defer ticker.Stop()

	// Initial poll
	s.poll(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.poll(ctx)
		}
	}
}

func (s *Syncer) poll(ctx context.Context) {
	desired, err := s.fetchDesiredState(ctx)
	if err != nil {
		s.log.WarnContext(ctx, "desired-state sync: failed to fetch", "error", err)
		return
	}

	s.reconcile(ctx, desired)
}

func (s *Syncer) fetchDesiredState(ctx context.Context) (*desiredstate.DesiredState, error) {
	u := fmt.Sprintf("%s/exelet-desired?host=%s", s.exedURL, url.QueryEscape(s.exeletAddr))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	var ds desiredstate.DesiredState
	if err := json.NewDecoder(resp.Body).Decode(&ds); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return &ds, nil
}

func (s *Syncer) reconcile(ctx context.Context, desired *desiredstate.DesiredState) {
	// Reconcile group-level cgroups
	for _, group := range desired.Groups {
		groupSlicePath := s.groupSlicePath(group.Name)
		for _, cg := range group.Cgroup {
			s.reconcileCgroupFile(ctx, groupSlicePath, cg.Path, cg.Value, "group", group.Name)
		}
	}

	// Track which VMs exed knows about vs what's on disk
	knownVMs := make(map[string]bool, len(desired.VMs))

	// Reconcile per-VM cgroups
	for _, vm := range desired.VMs {
		knownVMs[vm.ID] = true
		vmScopePath := s.vmScopePath(vm.ID, vm.Group)

		// Only reconcile cgroups if the scope directory exists
		// (the resource manager creates it when the VM process is running)
		if _, err := os.Stat(vmScopePath); os.IsNotExist(err) {
			// VM scope doesn't exist yet; the resource manager hasn't created it.
			// This is normal during VM startup or if VM is stopped.
			continue
		}

		for _, cg := range vm.Cgroup {
			if desiredstate.HasIOMaxPlaceholder(cg.Path, cg.Value) {
				s.reconcileIOMax(ctx, vmScopePath, vm.ID, cg.Value)
			} else {
				s.reconcileCgroupFile(ctx, vmScopePath, cg.Path, cg.Value, "vm", vm.ID)
			}
		}
	}

	// Identify VMs on disk that exed doesn't know about.
	// We log these but don't take action yet.
	s.reportUnknownVMs(ctx, desired.Groups, knownVMs)
}

func (s *Syncer) reconcileCgroupFile(ctx context.Context, cgroupDir, filename, desiredValue, entityType, entityID string) {
	// Reject filenames that could escape the cgroup directory.
	if strings.Contains(filename, "/") || strings.Contains(filename, "..") {
		s.log.WarnContext(ctx, "desired-state sync: rejecting invalid cgroup filename",
			"filename", filename, entityType, entityID)
		return
	}
	fullPath := filepath.Join(cgroupDir, filename)

	current, err := os.ReadFile(fullPath)
	if err != nil {
		s.log.WarnContext(ctx, "desired-state sync: cannot read cgroup file, skipping",
			"path", fullPath, "error", err,
			entityType, entityID)
		return
	}

	// Compare ignoring trailing whitespace
	currentTrimmed := strings.TrimRight(string(current), " \t\n\r")
	desiredTrimmed := strings.TrimRight(desiredValue, " \t\n\r")

	if currentTrimmed == desiredTrimmed {
		return // already matches
	}

	// Diff detected — write the desired value
	s.log.InfoContext(ctx, "desired-state sync: updating cgroup file",
		"path", fullPath,
		"current", currentTrimmed,
		"desired", desiredTrimmed,
		entityType, entityID)

	if err := os.WriteFile(fullPath, []byte(desiredValue), 0o644); err != nil {
		s.log.WarnContext(ctx, "desired-state sync: failed to write cgroup file",
			"path", fullPath, "error", err)
	}
}

// groupSlicePath returns the cgroup path for an account-level group slice.
func (s *Syncer) groupSlicePath(groupID string) string {
	if groupID == "" {
		groupID = "default"
	}
	sliceName := fmt.Sprintf("%s.slice", sanitizeCgroupName(groupID))
	return filepath.Join(s.cgroupRoot, cgroupSlice, sliceName)
}

// vmScopePath returns the cgroup path for a VM scope within its group.
func (s *Syncer) vmScopePath(vmID, groupID string) string {
	if groupID == "" {
		groupID = "default"
	}
	sliceName := fmt.Sprintf("%s.slice", sanitizeCgroupName(groupID))
	scopeName := fmt.Sprintf("vm-%s.scope", sanitizeCgroupName(vmID))
	return filepath.Join(s.cgroupRoot, cgroupSlice, sliceName, scopeName)
}

// reportUnknownVMs logs VMs found on disk that exed doesn't know about.
func (s *Syncer) reportUnknownVMs(ctx context.Context, groups []desiredstate.Group, knownVMs map[string]bool) {
	slicePath := filepath.Join(s.cgroupRoot, cgroupSlice)

	// Scan all group slices
	groupEntries, err := os.ReadDir(slicePath)
	if err != nil {
		return // exelet.slice may not exist yet
	}

	for _, ge := range groupEntries {
		if !ge.IsDir() || !strings.HasSuffix(ge.Name(), ".slice") {
			continue
		}

		groupPath := filepath.Join(slicePath, ge.Name())
		vmEntries, err := os.ReadDir(groupPath)
		if err != nil {
			continue
		}

		for _, ve := range vmEntries {
			if !ve.IsDir() || !strings.HasPrefix(ve.Name(), "vm-") || !strings.HasSuffix(ve.Name(), ".scope") {
				continue
			}

			// Extract VM ID from "vm-{id}.scope"
			vmID := strings.TrimPrefix(ve.Name(), "vm-")
			vmID = strings.TrimSuffix(vmID, ".scope")

			if !knownVMs[vmID] {
				s.log.WarnContext(ctx, "desired-state sync: VM on disk not known to exed",
					"vm_id", vmID,
					"cgroup_path", filepath.Join(groupPath, ve.Name()))
			}
		}
	}
}

// reconcileIOMax handles io.max.rbps and io.max.wbps virtual settings for a VM.
// It resolves the VM's block device, builds an io.max line, and writes the io.max file.
// reconcileIOMax handles an io.max setting with the ~ device placeholder.
// It resolves ~ to the actual MAJ:MIN, parses the key=value pairs from the
// placeholder value, and writes the io.max cgroup file.
func (s *Syncer) reconcileIOMax(ctx context.Context, vmScopePath, vmID, placeholderValue string) {
	if s.deviceResolver == nil {
		s.log.WarnContext(ctx, "desired-state sync: skipping io.max (no device resolver)",
			"vm", vmID)
		return
	}

	// Parse "~ rbps=X wbps=Y" into key=value updates.
	updates := parseIOMaxPlaceholder(placeholderValue)
	if len(updates) == 0 {
		return
	}

	majMin, err := s.deviceResolver.ResolveDevice(ctx, vmID)
	if err != nil {
		s.log.WarnContext(ctx, "desired-state sync: failed to resolve device for io.max",
			"vm", vmID, "error", err)
		return
	}

	ioMaxFile := filepath.Join(vmScopePath, "io.max")
	if err := updateIOMaxLine(ioMaxFile, majMin, updates); err != nil {
		s.log.WarnContext(ctx, "desired-state sync: failed to update io.max",
			"path", ioMaxFile, "vm", vmID, "error", err)
		return
	}

	s.log.InfoContext(ctx, "desired-state sync: updated io.max",
		"path", ioMaxFile, "vm", vmID, "device", majMin, "updates", updates)
}

// parseIOMaxPlaceholder parses "~ rbps=X wbps=Y" into a map of key→value.
func parseIOMaxPlaceholder(value string) map[string]string {
	updates := make(map[string]string)
	for _, field := range strings.Fields(value) {
		if field == desiredstate.IOMaxDevicePlaceholder {
			continue
		}
		if k, v, ok := strings.Cut(field, "="); ok {
			updates[k] = v
		}
	}
	return updates
}

// updateIOMaxLine updates specific keys for a device in io.max,
// preserving lines for other devices and other keys for this device.
func updateIOMaxLine(ioMaxFile, deviceMajMin string, updates map[string]string) error {
	existing, err := os.ReadFile(ioMaxFile)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read io.max: %w", err)
	}

	var lines []string
	found := false
	prefix := deviceMajMin + " "

	for _, line := range strings.Split(string(existing), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, prefix) {
			merged := mergeIOMaxKeys(line, deviceMajMin, updates)
			lines = append(lines, merged)
			found = true
		} else {
			lines = append(lines, line)
		}
	}

	if !found {
		lines = append(lines, buildIOMaxLine(deviceMajMin, updates))
	}

	desired := strings.Join(lines, "\n")

	// Compare with current content to avoid unnecessary writes.
	currentTrimmed := strings.TrimRight(string(existing), " \t\n\r")
	if currentTrimmed == desired {
		return nil
	}

	return os.WriteFile(ioMaxFile, []byte(desired), 0o644)
}

// mergeIOMaxKeys parses an existing io.max line and merges in the updates,
// preserving any keys not being updated.
func mergeIOMaxKeys(existingLine, deviceMajMin string, updates map[string]string) string {
	parts := strings.Fields(existingLine)
	if len(parts) < 1 {
		return buildIOMaxLine(deviceMajMin, updates)
	}

	existing := make(map[string]string)
	for _, part := range parts[1:] {
		if kv := strings.SplitN(part, "=", 2); len(kv) == 2 {
			existing[kv[0]] = kv[1]
		}
	}

	for k, v := range updates {
		existing[k] = v
	}

	return buildIOMaxLine(deviceMajMin, existing)
}

// buildIOMaxLine constructs an io.max line from device and key=value pairs.
func buildIOMaxLine(deviceMajMin string, kvs map[string]string) string {
	var parts []string
	parts = append(parts, deviceMajMin)

	// Standard key order for consistent output.
	for _, key := range []string{"rbps", "wbps", "riops", "wiops"} {
		if val, ok := kvs[key]; ok {
			parts = append(parts, key+"="+val)
		}
	}
	for key, val := range kvs {
		switch key {
		case "rbps", "wbps", "riops", "wiops":
			continue
		default:
			parts = append(parts, key+"="+val)
		}
	}

	return strings.Join(parts, " ")
}

// sanitizeCgroupName matches the resource manager's sanitization.
func sanitizeCgroupName(id string) string {
	return strings.ReplaceAll(id, "/", "_")
}
