package desiredsync

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"exe.dev/desiredstate"
)

func TestFetchDesiredState(t *testing.T) {
	expected := desiredstate.DesiredState{
		Groups: []desiredstate.Group{
			{Name: "user1", Cgroup: []desiredstate.CgroupSetting{}},
		},
		VMs: []desiredstate.VM{
			{
				ID:    "vm-001",
				Group: "user1",
				State: "running",
				Cgroup: []desiredstate.CgroupSetting{
					{Path: "cpu.max", Value: "50000 100000"},
				},
			},
		},
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/exelet-desired" {
			t.Errorf("unexpected path: %s", r.URL.Path)
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		host := r.URL.Query().Get("host")
		if host != "tcp://myhost:9080" {
			t.Errorf("unexpected host param: %s", host)
			http.Error(w, "bad host", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(expected)
	}))
	defer ts.Close()

	syncer, err := New(Config{
		ExedURL:    ts.URL,
		ExeletAddr: "tcp://myhost:9080",
	}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}

	got, err := syncer.fetchDesiredState(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if len(got.VMs) != 1 || got.VMs[0].ID != "vm-001" {
		t.Errorf("unexpected VMs: %+v", got.VMs)
	}
	if len(got.Groups) != 1 || got.Groups[0].Name != "user1" {
		t.Errorf("unexpected Groups: %+v", got.Groups)
	}
}

func TestFetchDesiredStateHTTPError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "server error", http.StatusInternalServerError)
	}))
	defer ts.Close()

	syncer, err := New(Config{
		ExedURL:    ts.URL,
		ExeletAddr: "tcp://myhost:9080",
	}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}

	_, err = syncer.fetchDesiredState(context.Background())
	if err == nil {
		t.Error("expected error for 500 response")
	}
}

func TestFetchDesiredStateTimeout(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer ts.Close()

	syncer, err := New(Config{
		ExedURL:    ts.URL,
		ExeletAddr: "tcp://myhost:9080",
	}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	// Override HTTP client with a short timeout
	syncer.httpClient = &http.Client{Timeout: 100 * time.Millisecond}

	_, err = syncer.fetchDesiredState(context.Background())
	if err == nil {
		t.Error("expected timeout error")
	}
}

func TestReconcileCgroupFile(t *testing.T) {
	tmpDir := t.TempDir()
	syncer := &Syncer{log: slog.Default(), cgroupRoot: tmpDir}

	dir := filepath.Join(tmpDir, "test-scope")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()

	// Test: skip when file doesn't exist
	syncer.reconcileCgroupFile(ctx, dir, "cpu.max", "50000 100000", "vm", "test-id")
	if _, err := os.Stat(filepath.Join(dir, "cpu.max")); !os.IsNotExist(err) {
		t.Error("expected file to not be created when it doesn't exist")
	}

	// Test: no-op when value matches (with trailing newline)
	os.WriteFile(filepath.Join(dir, "cpu.max"), []byte("50000 100000\n"), 0o644)
	syncer.reconcileCgroupFile(ctx, dir, "cpu.max", "50000 100000", "vm", "test-id")
	// Should not have changed (still has trailing newline since no write needed)
	data, _ := os.ReadFile(filepath.Join(dir, "cpu.max"))
	if string(data) != "50000 100000\n" {
		t.Errorf("file should not have been rewritten, got %q", string(data))
	}

	// Test: update when value differs
	syncer.reconcileCgroupFile(ctx, dir, "cpu.max", "max 100000", "vm", "test-id")
	data, _ = os.ReadFile(filepath.Join(dir, "cpu.max"))
	if string(data) != "max 100000" {
		t.Errorf("expected 'max 100000', got %q", string(data))
	}
}

func TestReconcileRejectsPathTraversal(t *testing.T) {
	tmpDir := t.TempDir()
	syncer := &Syncer{log: slog.Default(), cgroupRoot: tmpDir}

	dir := filepath.Join(tmpDir, "test-scope")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create a file that should NOT be touched
	target := filepath.Join(tmpDir, "secret")
	os.WriteFile(target, []byte("original"), 0o644)

	ctx := context.Background()

	// Attempts with path traversal should be rejected
	for _, bad := range []string{"../secret", "foo/../../../secret", "sub/cpu.max"} {
		syncer.reconcileCgroupFile(ctx, dir, bad, "pwned", "vm", "test-id")
	}

	data, _ := os.ReadFile(target)
	if string(data) != "original" {
		t.Errorf("path traversal succeeded, file contains %q", string(data))
	}
}

func TestReconcileWritesOnlyWhenDiff(t *testing.T) {
	tmpDir := t.TempDir()

	// Set up a fake cgroup filesystem
	cgRoot := tmpDir
	slicePath := filepath.Join(cgRoot, cgroupSlice, "user1.slice", "vm-vm001.scope")
	if err := os.MkdirAll(slicePath, 0o755); err != nil {
		t.Fatal(err)
	}

	// Pre-populate with matching value
	os.WriteFile(filepath.Join(slicePath, "cpu.max"), []byte("50000 100000\n"), 0o644)
	// Pre-populate with different value
	os.WriteFile(filepath.Join(slicePath, "memory.high"), []byte("999999\n"), 0o644)

	syncer := &Syncer{log: slog.Default(), cgroupRoot: cgRoot}

	ds := &desiredstate.DesiredState{
		Groups: []desiredstate.Group{
			{Name: "user1", Cgroup: []desiredstate.CgroupSetting{}},
		},
		VMs: []desiredstate.VM{
			{
				ID:    "vm001",
				Group: "user1",
				State: "running",
				Cgroup: []desiredstate.CgroupSetting{
					{Path: "cpu.max", Value: "50000 100000"},
					{Path: "memory.high", Value: "1234567"},
				},
			},
		},
	}

	syncer.reconcile(context.Background(), ds)

	// cpu.max should still have trailing newline (not rewritten)
	data, _ := os.ReadFile(filepath.Join(slicePath, "cpu.max"))
	if string(data) != "50000 100000\n" {
		t.Errorf("cpu.max should not have been rewritten, got %q", string(data))
	}

	// memory.high should have been updated
	data, _ = os.ReadFile(filepath.Join(slicePath, "memory.high"))
	if string(data) != "1234567" {
		t.Errorf("memory.high should be '1234567', got %q", string(data))
	}
}

func TestReconcileSkipsMissingVMScope(t *testing.T) {
	tmpDir := t.TempDir()
	syncer := &Syncer{log: slog.Default(), cgroupRoot: tmpDir}

	// Don't create the scope directory. Reconcile should not panic or error.
	ds := &desiredstate.DesiredState{
		Groups: []desiredstate.Group{{Name: "user1"}},
		VMs: []desiredstate.VM{
			{
				ID:    "vm-nonexistent",
				Group: "user1",
				State: "running",
				Cgroup: []desiredstate.CgroupSetting{
					{Path: "cpu.max", Value: "max 100000"},
				},
			},
		},
	}

	// Should not panic
	syncer.reconcile(context.Background(), ds)
}

func TestReportUnknownVMs(t *testing.T) {
	tmpDir := t.TempDir()

	// Create some VM scopes on disk
	slicePath := filepath.Join(tmpDir, cgroupSlice)
	groupPath := filepath.Join(slicePath, "user1.slice")
	for _, vmID := range []string{"vm-known.scope", "vm-unknown.scope"} {
		if err := os.MkdirAll(filepath.Join(groupPath, vmID), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// Capture log output
	var logBuf strings.Builder
	logHandler := slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelWarn})
	logger := slog.New(logHandler)

	syncer := &Syncer{log: logger, cgroupRoot: tmpDir}

	knownVMs := map[string]bool{"known": true}
	syncer.reportUnknownVMs(context.Background(), nil, knownVMs)

	if !strings.Contains(logBuf.String(), "unknown") {
		t.Errorf("expected warning about unknown VM, got: %s", logBuf.String())
	}
	if strings.Contains(logBuf.String(), "known") && !strings.Contains(logBuf.String(), "unknown") {
		t.Errorf("should not warn about known VM")
	}
}

func TestSyncerStartStop(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(desiredstate.DesiredState{})
	}))
	defer ts.Close()

	syncer, err := New(Config{
		ExedURL:      ts.URL,
		ExeletAddr:   "tcp://myhost:9080",
		CgroupRoot:   t.TempDir(),
		PollInterval: 50 * time.Millisecond,
	}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}

	if err := syncer.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Let it poll a couple of times
	time.Sleep(150 * time.Millisecond)

	// Stop should not hang
	syncer.Stop()
}

func TestNewSyncerRequiresExedURL(t *testing.T) {
	_, err := New(Config{ExeletAddr: "tcp://x:9080"}, slog.Default())
	if err == nil {
		t.Error("expected error when ExedURL is empty")
	}
}

func TestNewSyncerRequiresExeletAddr(t *testing.T) {
	_, err := New(Config{ExedURL: "http://localhost:8080"}, slog.Default())
	if err == nil {
		t.Error("expected error when ExeletAddr is empty")
	}
}

func TestGroupSlicePath(t *testing.T) {
	s := &Syncer{cgroupRoot: "/sys/fs/cgroup"}

	tests := []struct {
		groupID  string
		expected string
	}{
		{"user123", "/sys/fs/cgroup/exelet.slice/user123.slice"},
		{"", "/sys/fs/cgroup/exelet.slice/default.slice"},
		{"user/with/slashes", "/sys/fs/cgroup/exelet.slice/user_with_slashes.slice"},
	}

	for _, tt := range tests {
		got := s.groupSlicePath(tt.groupID)
		if got != tt.expected {
			t.Errorf("groupSlicePath(%q) = %q, want %q", tt.groupID, got, tt.expected)
		}
	}
}

func TestVmScopePath(t *testing.T) {
	s := &Syncer{cgroupRoot: "/sys/fs/cgroup"}

	got := s.vmScopePath("vm001", "user1")
	expected := "/sys/fs/cgroup/exelet.slice/user1.slice/vm-vm001.scope"
	if got != expected {
		t.Errorf("vmScopePath = %q, want %q", got, expected)
	}

	got = s.vmScopePath("vm002", "")
	expected = "/sys/fs/cgroup/exelet.slice/default.slice/vm-vm002.scope"
	if got != expected {
		t.Errorf("vmScopePath (empty group) = %q, want %q", got, expected)
	}
}

func TestRefresh(t *testing.T) {
	calls := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(desiredstate.DesiredState{})
	}))
	defer ts.Close()

	syncer, err := New(Config{
		ExedURL:      ts.URL,
		ExeletAddr:   "tcp://myhost:9080",
		CgroupRoot:   t.TempDir(),
		PollInterval: 1 * time.Hour, // very long so we don't get timer polls
	}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}

	syncer.Refresh(context.Background())
	if calls != 1 {
		t.Errorf("expected 1 call, got %d", calls)
	}

	syncer.Refresh(context.Background())
	if calls != 2 {
		t.Errorf("expected 2 calls, got %d", calls)
	}
}

// mockDeviceResolver is a test implementation of DeviceResolver.
type mockDeviceResolver struct {
	devices map[string]string // vmID -> "MAJ:MIN"
	err     error
}

func (m *mockDeviceResolver) ResolveDevice(_ context.Context, vmID string) (string, error) {
	if m.err != nil {
		return "", m.err
	}
	if majMin, ok := m.devices[vmID]; ok {
		return majMin, nil
	}
	return "", fmt.Errorf("device not found for VM %s", vmID)
}

func TestReconcileIOMaxPlaceholder(t *testing.T) {
	tmpDir := t.TempDir()

	cgRoot := tmpDir
	slicePath := filepath.Join(cgRoot, cgroupSlice, "user1.slice", "vm-vm001.scope")
	if err := os.MkdirAll(slicePath, 0o755); err != nil {
		t.Fatal(err)
	}

	// Pre-create io.max file (cgroup fs always has it)
	os.WriteFile(filepath.Join(slicePath, "io.max"), []byte(""), 0o644)
	// Pre-create cpu.max
	os.WriteFile(filepath.Join(slicePath, "cpu.max"), []byte("max 100000\n"), 0o644)

	resolver := &mockDeviceResolver{
		devices: map[string]string{"vm001": "8:0"},
	}

	syncer := &Syncer{
		log:            slog.Default(),
		cgroupRoot:     cgRoot,
		deviceResolver: resolver,
	}

	ds := &desiredstate.DesiredState{
		Groups: []desiredstate.Group{
			{Name: "user1", Cgroup: []desiredstate.CgroupSetting{}},
		},
		VMs: []desiredstate.VM{
			{
				ID:    "vm001",
				Group: "user1",
				State: "running",
				Cgroup: []desiredstate.CgroupSetting{
					{Path: "cpu.max", Value: "50000 100000"},
					{Path: "io.max", Value: "~ rbps=10485760 wbps=52428800"},
				},
			},
		},
	}

	syncer.reconcile(context.Background(), ds)

	// cpu.max should have been updated
	data, _ := os.ReadFile(filepath.Join(slicePath, "cpu.max"))
	if string(data) != "50000 100000" {
		t.Errorf("cpu.max = %q, want %q", string(data), "50000 100000")
	}

	// io.max should have the resolved device line
	data, _ = os.ReadFile(filepath.Join(slicePath, "io.max"))
	ioMaxContent := string(data)
	if !strings.Contains(ioMaxContent, "8:0") {
		t.Errorf("io.max should contain device 8:0, got %q", ioMaxContent)
	}
	if !strings.Contains(ioMaxContent, "rbps=10485760") {
		t.Errorf("io.max should contain rbps=10485760, got %q", ioMaxContent)
	}
	if !strings.Contains(ioMaxContent, "wbps=52428800") {
		t.Errorf("io.max should contain wbps=52428800, got %q", ioMaxContent)
	}
}

func TestReconcileIOMaxNoDeviceResolver(t *testing.T) {
	tmpDir := t.TempDir()

	cgRoot := tmpDir
	slicePath := filepath.Join(cgRoot, cgroupSlice, "user1.slice", "vm-vm001.scope")
	if err := os.MkdirAll(slicePath, 0o755); err != nil {
		t.Fatal(err)
	}

	os.WriteFile(filepath.Join(slicePath, "io.max"), []byte(""), 0o644)

	// Capture log output to verify warning.
	var logBuf strings.Builder
	logHandler := slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelWarn})
	logger := slog.New(logHandler)

	syncer := &Syncer{
		log:            logger,
		cgroupRoot:     cgRoot,
		deviceResolver: nil, // no resolver
	}

	ds := &desiredstate.DesiredState{
		Groups: []desiredstate.Group{
			{Name: "user1", Cgroup: []desiredstate.CgroupSetting{}},
		},
		VMs: []desiredstate.VM{
			{
				ID:    "vm001",
				Group: "user1",
				State: "running",
				Cgroup: []desiredstate.CgroupSetting{
					{Path: "io.max", Value: "~ rbps=10485760"},
				},
			},
		},
	}

	syncer.reconcile(context.Background(), ds)

	// io.max should NOT have been modified.
	data, _ := os.ReadFile(filepath.Join(slicePath, "io.max"))
	if string(data) != "" {
		t.Errorf("io.max should be empty (resolver is nil), got %q", string(data))
	}

	// Should have logged a warning.
	if !strings.Contains(logBuf.String(), "no device resolver") {
		t.Errorf("expected warning about no device resolver, got: %s", logBuf.String())
	}
}

func TestReconcileIOMaxPreservesOtherDevices(t *testing.T) {
	tmpDir := t.TempDir()

	cgRoot := tmpDir
	slicePath := filepath.Join(cgRoot, cgroupSlice, "user1.slice", "vm-vm001.scope")
	if err := os.MkdirAll(slicePath, 0o755); err != nil {
		t.Fatal(err)
	}

	// Pre-populate io.max with a line for a different device.
	os.WriteFile(filepath.Join(slicePath, "io.max"), []byte("252:0 rbps=max wbps=max\n"), 0o644)

	resolver := &mockDeviceResolver{
		devices: map[string]string{"vm001": "8:0"},
	}

	syncer := &Syncer{
		log:            slog.Default(),
		cgroupRoot:     cgRoot,
		deviceResolver: resolver,
	}

	ds := &desiredstate.DesiredState{
		Groups: []desiredstate.Group{
			{Name: "user1", Cgroup: []desiredstate.CgroupSetting{}},
		},
		VMs: []desiredstate.VM{
			{
				ID:    "vm001",
				Group: "user1",
				State: "running",
				Cgroup: []desiredstate.CgroupSetting{
					{Path: "io.max", Value: "~ rbps=5242880"},
				},
			},
		},
	}

	syncer.reconcile(context.Background(), ds)

	data, _ := os.ReadFile(filepath.Join(slicePath, "io.max"))
	ioMaxContent := string(data)

	// Should preserve the existing device line.
	if !strings.Contains(ioMaxContent, "252:0") {
		t.Errorf("io.max should preserve device 252:0, got %q", ioMaxContent)
	}
	// Should have the new device line.
	if !strings.Contains(ioMaxContent, "8:0 rbps=5242880") {
		t.Errorf("io.max should contain 8:0 rbps=5242880, got %q", ioMaxContent)
	}
}

func TestReconcileIOMaxClearOverrides(t *testing.T) {
	tmpDir := t.TempDir()

	cgRoot := tmpDir
	slicePath := filepath.Join(cgRoot, cgroupSlice, "user1.slice", "vm-vm001.scope")
	if err := os.MkdirAll(slicePath, 0o755); err != nil {
		t.Fatal(err)
	}

	// Pre-populate with existing throttle.
	os.WriteFile(filepath.Join(slicePath, "io.max"), []byte("8:0 rbps=10485760 wbps=10485760"), 0o644)

	resolver := &mockDeviceResolver{
		devices: map[string]string{"vm001": "8:0"},
	}

	syncer := &Syncer{
		log:            slog.Default(),
		cgroupRoot:     cgRoot,
		deviceResolver: resolver,
	}

	// --io=clear produces "~ rbps=max wbps=max".
	ds := &desiredstate.DesiredState{
		Groups: []desiredstate.Group{
			{Name: "user1", Cgroup: []desiredstate.CgroupSetting{}},
		},
		VMs: []desiredstate.VM{
			{
				ID:    "vm001",
				Group: "user1",
				State: "running",
				Cgroup: []desiredstate.CgroupSetting{
					{Path: "io.max", Value: "~ rbps=max wbps=max"},
				},
			},
		},
	}

	syncer.reconcile(context.Background(), ds)

	data, _ := os.ReadFile(filepath.Join(slicePath, "io.max"))
	ioMaxContent := string(data)
	if !strings.Contains(ioMaxContent, "rbps=max") {
		t.Errorf("io.max should contain rbps=max after clear, got %q", ioMaxContent)
	}
	if !strings.Contains(ioMaxContent, "wbps=max") {
		t.Errorf("io.max should contain wbps=max after clear, got %q", ioMaxContent)
	}
}

func TestUpdateIOMaxLine(t *testing.T) {
	tmpDir := t.TempDir()
	ioMaxFile := filepath.Join(tmpDir, "io.max")

	// Test: create new file with updates.
	err := updateIOMaxLine(ioMaxFile, "8:0", map[string]string{"rbps": "1048576", "wbps": "2097152"})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(ioMaxFile)
	if string(data) != "8:0 rbps=1048576 wbps=2097152" {
		t.Errorf("unexpected content: %q", string(data))
	}

	// Test: merge with existing (preserve riops key).
	os.WriteFile(ioMaxFile, []byte("8:0 rbps=500 wbps=500 riops=100"), 0o644)
	err = updateIOMaxLine(ioMaxFile, "8:0", map[string]string{"rbps": "1000"})
	if err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(ioMaxFile)
	content := string(data)
	if !strings.Contains(content, "rbps=1000") {
		t.Errorf("expected rbps=1000, got %q", content)
	}
	if !strings.Contains(content, "wbps=500") {
		t.Errorf("expected wbps=500 preserved, got %q", content)
	}
	if !strings.Contains(content, "riops=100") {
		t.Errorf("expected riops=100 preserved, got %q", content)
	}

	// Test: no-op when already matching.
	os.WriteFile(ioMaxFile, []byte("8:0 rbps=1000 wbps=2000"), 0o644)
	info1, _ := os.Stat(ioMaxFile)
	err = updateIOMaxLine(ioMaxFile, "8:0", map[string]string{"rbps": "1000", "wbps": "2000"})
	if err != nil {
		t.Fatal(err)
	}
	info2, _ := os.Stat(ioMaxFile)
	// ModTime comparison isn't reliable in fast tests; just check content is unchanged.
	_ = info1
	_ = info2
	data, _ = os.ReadFile(ioMaxFile)
	if string(data) != "8:0 rbps=1000 wbps=2000" {
		t.Errorf("content should be unchanged, got %q", string(data))
	}
}
