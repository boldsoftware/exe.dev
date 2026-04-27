package resourcemanager

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"exe.dev/exelet/config"
)

// TestPrepareVMCgroup_DefaultGroup verifies that PrepareVMCgroup creates the
// exelet.slice/default.slice/vm-<id>.scope directory under the configured
// cgroup root, so cloud-hypervisor can be spawned directly into it with
// CLONE_INTO_CGROUP.
func TestPrepareVMCgroup_DefaultGroup(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("cgroup v2 only on linux")
	}

	root := t.TempDir()
	// Satisfy the v2 check: the preparer looks for cgroup.controllers at root.
	if err := os.WriteFile(filepath.Join(root, "cgroup.controllers"), []byte("cpu memory\n"), 0o644); err != nil {
		t.Fatalf("write cgroup.controllers: %v", err)
	}

	m := &ResourceManager{
		config:     &config.ExeletConfig{},
		log:        slog.Default(),
		cgroupRoot: root,
	}

	got, err := m.PrepareVMCgroup(context.Background(), "vmabc", "")
	if err != nil {
		t.Fatalf("PrepareVMCgroup: %v", err)
	}
	want := filepath.Join(root, "exelet.slice", "default.slice", "vm-vmabc.scope")
	if got != want {
		t.Fatalf("path = %q, want %q", got, want)
	}
	fi, err := os.Stat(got)
	if err != nil {
		t.Fatalf("scope dir not created: %v", err)
	}
	if !fi.IsDir() {
		t.Fatalf("scope path is not a directory")
	}

	// Second call should be idempotent.
	got2, err := m.PrepareVMCgroup(context.Background(), "vmabc", "")
	if err != nil {
		t.Fatalf("second PrepareVMCgroup: %v", err)
	}
	if got2 != got {
		t.Fatalf("second path = %q, want %q", got2, got)
	}
}

// TestPrepareVMCgroup_GroupID verifies per-account slice placement.
func TestPrepareVMCgroup_GroupID(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("cgroup v2 only on linux")
	}

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "cgroup.controllers"), []byte("cpu memory\n"), 0o644); err != nil {
		t.Fatalf("write cgroup.controllers: %v", err)
	}

	m := &ResourceManager{
		config:     &config.ExeletConfig{},
		log:        slog.Default(),
		cgroupRoot: root,
	}

	got, err := m.PrepareVMCgroup(context.Background(), "vmq", "acct_42")
	if err != nil {
		t.Fatalf("PrepareVMCgroup: %v", err)
	}
	want := filepath.Join(root, "exelet.slice", "acct_42.slice", "vm-vmq.scope")
	if got != want {
		t.Fatalf("path = %q, want %q", got, want)
	}
}

// TestPrepareVMCgroup_NoV2 returns an empty path (no error) when cgroup v2 is
// not mounted under the configured root, so callers fall back to the legacy
// "start in root, move later" behavior.
func TestPrepareVMCgroup_NoV2(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("linux-only code path")
	}

	root := t.TempDir() // no cgroup.controllers file

	m := &ResourceManager{
		config:     &config.ExeletConfig{},
		log:        slog.Default(),
		cgroupRoot: root,
	}

	got, err := m.PrepareVMCgroup(context.Background(), "vmx", "")
	if err != nil {
		t.Fatalf("PrepareVMCgroup: %v", err)
	}
	if got != "" {
		t.Fatalf("expected empty path without cgroup v2; got %q", got)
	}
}

// TestReleaseVMCgroup_RemovesScope verifies that ReleaseVMCgroup removes the
// vm-<id>.scope directory created by PrepareVMCgroup, so failed VM creations
// don't leak empty scope dirs.
func TestReleaseVMCgroup_RemovesScope(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("cgroup v2 only on linux")
	}

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "cgroup.controllers"), []byte("cpu memory\n"), 0o644); err != nil {
		t.Fatalf("write cgroup.controllers: %v", err)
	}

	m := &ResourceManager{
		config:     &config.ExeletConfig{},
		log:        slog.Default(),
		cgroupRoot: root,
		usageState: map[string]*vmUsageState{},
	}

	scope, err := m.PrepareVMCgroup(context.Background(), "vmrel", "acct1")
	if err != nil {
		t.Fatalf("PrepareVMCgroup: %v", err)
	}
	if _, err := os.Stat(scope); err != nil {
		t.Fatalf("scope not created: %v", err)
	}

	if err := m.ReleaseVMCgroup(context.Background(), "vmrel", "acct1"); err != nil {
		t.Fatalf("ReleaseVMCgroup: %v", err)
	}
	if _, err := os.Stat(scope); !os.IsNotExist(err) {
		t.Fatalf("scope still exists after release: err=%v", err)
	}
}

// TestReleaseVMCgroup_Idempotent verifies that ReleaseVMCgroup is safe to
// call when the scope was never created (rollback path before
// PrepareVMCgroup runs) and when cgroup v2 is unavailable.
func TestReleaseVMCgroup_Idempotent(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("linux-only")
	}

	root := t.TempDir()
	m := &ResourceManager{
		config:     &config.ExeletConfig{},
		log:        slog.Default(),
		cgroupRoot: root,
		usageState: map[string]*vmUsageState{},
	}
	// No cgroup.controllers => v2 unavailable => no-op.
	if err := m.ReleaseVMCgroup(context.Background(), "ghost", ""); err != nil {
		t.Fatalf("release without v2: %v", err)
	}
	// With v2 but never-prepared scope: still ok (removeCgroup tolerates missing).
	if err := os.WriteFile(filepath.Join(root, "cgroup.controllers"), []byte("cpu memory\n"), 0o644); err != nil {
		t.Fatalf("write cgroup.controllers: %v", err)
	}
	if err := m.ReleaseVMCgroup(context.Background(), "ghost", ""); err != nil {
		t.Fatalf("release of nonexistent scope: %v", err)
	}
}
