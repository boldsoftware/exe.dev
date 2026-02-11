package desiredsync

import (
	"context"
	"encoding/json"
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
