//go:build linux

package cloudhypervisor

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func newTestVMM(t *testing.T) *VMM {
	t.Helper()
	return &VMM{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func TestApplyCgroupPlacement_NoProvider(t *testing.T) {
	v := newTestVMM(t)
	sys := &syscall.SysProcAttr{Setpgid: true}
	cleanup, err := v.applyCgroupPlacement(context.Background(), "vm-x", sys)
	if err != nil {
		t.Fatalf("applyCgroupPlacement: %v", err)
	}
	t.Cleanup(cleanup)
	if sys.UseCgroupFD {
		t.Fatalf("expected UseCgroupFD=false when no provider configured; got true")
	}
}

func TestApplyCgroupPlacement_EmptyPath(t *testing.T) {
	v := newTestVMM(t)
	v.cgroupPathFunc = func(context.Context, string) (string, error) {
		return "", nil
	}
	sys := &syscall.SysProcAttr{Setpgid: true}
	cleanup, err := v.applyCgroupPlacement(context.Background(), "vm-x", sys)
	if err != nil {
		t.Fatalf("applyCgroupPlacement: %v", err)
	}
	t.Cleanup(cleanup)
	if sys.UseCgroupFD {
		t.Fatalf("expected UseCgroupFD=false on empty path; got true")
	}
}

// TestApplyCgroupPlacement_RealDir verifies the happy path: a directory exists,
// the VMM opens it, and sets UseCgroupFD with a valid fd pointing at that dir.
// We simulate the cgroup directory with a plain tmpdir; applyCgroupPlacement
// only needs an O_DIRECTORY-openable path.
func TestApplyCgroupPlacement_RealDir(t *testing.T) {
	dir := t.TempDir()
	// Create a nested dir to mimic vm-<id>.scope
	scope := filepath.Join(dir, "vm-abc.scope")
	if err := os.MkdirAll(scope, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	var gotID string
	v := newTestVMM(t)
	v.cgroupPathFunc = func(_ context.Context, id string) (string, error) {
		gotID = id
		return scope, nil
	}

	sys := &syscall.SysProcAttr{Setpgid: true}
	cleanup, err := v.applyCgroupPlacement(context.Background(), "abc", sys)
	if err != nil {
		t.Fatalf("applyCgroupPlacement: %v", err)
	}
	defer cleanup()

	if gotID != "abc" {
		t.Fatalf("provider got id=%q, want %q", gotID, "abc")
	}
	if !sys.UseCgroupFD {
		t.Fatalf("expected UseCgroupFD=true")
	}
	// Verify the fd is valid and points at the scope directory.
	// /proc/self/fd/<N> resolves to the opened path.
	link, err := os.Readlink(filepath.Join("/proc/self/fd", itoa(sys.CgroupFD)))
	if err != nil {
		t.Fatalf("readlink fd: %v", err)
	}
	// os.Readlink may return the absolute path after symlink resolution;
	// compare with EvalSymlinks on scope for robustness.
	wantResolved, err := filepath.EvalSymlinks(scope)
	if err != nil {
		t.Fatalf("evalsymlinks: %v", err)
	}
	gotResolved, _ := filepath.EvalSymlinks(link)
	if gotResolved == "" {
		gotResolved = link
	}
	if gotResolved != wantResolved {
		t.Fatalf("cgroup fd points at %q, want %q", gotResolved, wantResolved)
	}
	// Pgid flag should still be set (we didn't clobber the rest).
	if !sys.Setpgid {
		t.Fatalf("Setpgid was clobbered")
	}
}

// TestApplyCgroupPlacement_MissingDir tolerates a non-existent path by falling
// back (no UseCgroupFD), rather than failing to start the VM.
func TestApplyCgroupPlacement_MissingDir(t *testing.T) {
	v := newTestVMM(t)
	v.cgroupPathFunc = func(context.Context, string) (string, error) {
		return "/nonexistent/cgroup/path", nil
	}
	sys := &syscall.SysProcAttr{Setpgid: true}
	cleanup, err := v.applyCgroupPlacement(context.Background(), "abc", sys)
	if err != nil {
		t.Fatalf("applyCgroupPlacement: %v", err)
	}
	t.Cleanup(cleanup)
	if sys.UseCgroupFD {
		t.Fatalf("expected fallback (UseCgroupFD=false) on missing dir; got true")
	}
}

func itoa(n int) string {
	// Minimal local itoa to avoid importing strconv into a small test.
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
