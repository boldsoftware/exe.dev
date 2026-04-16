//go:build linux

package replication

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// These integration tests exercise processVolume against real (sparse-file
// backed) ZFS pools. They are skipped unless running as root with zfs/zpool
// available — see exelet/storage/zfs/tier_migration_test.go for the same
// pattern.

func skipIfNotRoot(t *testing.T) {
	t.Helper()
	if os.Getuid() != 0 {
		t.Skip("skipping: ZFS tests require root")
	}
}

func skipIfNoZFS(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("zpool"); err != nil {
		t.Skip("skipping: zpool not found in PATH")
	}
	if _, err := exec.LookPath("zfs"); err != nil {
		t.Skip("skipping: zfs not found in PATH")
	}
}

func skipReplicationIntegration(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping: replication integration tests are slow")
	}
	skipIfNotRoot(t)
	skipIfNoZFS(t)
}

// createTestPool creates an ephemeral ZFS pool backed by a sparse file of the
// given size (e.g. "256M", "16M"). Returns the pool name; cleanup is registered
// on t.
func createTestPool(t *testing.T, suffix, size string) string {
	t.Helper()

	randBytes := make([]byte, 4)
	if _, err := rand.Read(randBytes); err != nil {
		t.Fatalf("rand: %v", err)
	}
	poolName := fmt.Sprintf("repl-%s-%s", suffix, hex.EncodeToString(randBytes))

	imgDir := t.TempDir()
	imgPath := filepath.Join(imgDir, poolName+".img")

	if out, err := exec.Command("truncate", "-s", size, imgPath).CombinedOutput(); err != nil {
		t.Fatalf("truncate: %v (%s)", err, out)
	}
	if out, err := exec.Command("zpool", "create", poolName, imgPath).CombinedOutput(); err != nil {
		t.Fatalf("zpool create: %v (%s)", err, out)
	}

	t.Cleanup(func() {
		if out, err := exec.Command("zpool", "destroy", "-f", poolName).CombinedOutput(); err != nil {
			t.Logf("warning: zpool destroy %s: %v (%s)", poolName, err, out)
		}
	})
	return poolName
}

// fillDataset writes random data of the given size into a new file on the
// dataset's mountpoint, forcing a snapshot of that size to be non-trivial.
func fillDataset(t *testing.T, dataset string, mb int) {
	t.Helper()
	mp := strings.TrimSpace(runOrFail(t, "zfs", "get", "-H", "-o", "value", "mountpoint", dataset))
	if mp == "" || mp == "-" || mp == "none" {
		t.Fatalf("dataset %s has no mountpoint", dataset)
	}
	f, err := os.Create(filepath.Join(mp, "blob"))
	if err != nil {
		t.Fatalf("create blob: %v", err)
	}
	defer f.Close()
	if _, err := io.CopyN(f, rand.Reader, int64(mb)*1024*1024); err != nil {
		t.Fatalf("write blob: %v", err)
	}
	if err := f.Sync(); err != nil {
		t.Fatalf("sync blob: %v", err)
	}
}

func runOrFail(t *testing.T, name string, args ...string) string {
	t.Helper()
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v (%s)", name, args, err, out)
	}
	return string(out)
}

// newWorkerPoolForTest constructs a WorkerPool against the given target with
// minimal config suitable for tests. Cleanup is registered on t.
func newWorkerPoolForTest(t *testing.T, target Target, volumeTimeout time.Duration) (*WorkerPool, *State) {
	t.Helper()
	state, err := NewState(t.TempDir())
	if err != nil {
		t.Fatalf("NewState: %v", err)
	}
	metrics := NewMetrics(nil)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	wp := NewWorkerPool(target, state, metrics, 1, 1, volumeTimeout, log, func(string) bool { return false })
	t.Cleanup(func() { wp.Stop() })
	return wp, state
}

// TestProcessVolume_ENOSPC_PreservesRemoteBackup verifies that when the
// replication target is too small to receive a send, processVolume:
//  1. Records a failure tagged with ErrTargetFull's message.
//  2. Does NOT destroy the pre-existing remote dataset (Bug 4 — the fallback
//     destroy-and-full-resend would otherwise delete the only backup).
func TestProcessVolume_ENOSPC_PreservesRemoteBackup(t *testing.T) {
	skipReplicationIntegration(t)

	srcPool := createTestPool(t, "src", "256M")
	// Tiny target pool so the second send is guaranteed to overflow.
	dstPool := createTestPool(t, "dst", "64M")

	// Source dataset with enough data that a full send won't fit in dst.
	srcDataset := srcPool + "/vol"
	runOrFail(t, "zfs", "create", srcDataset)
	fillDataset(t, srcDataset, 32) // 32 MiB

	// Seed the destination with an initial successful replication so we have
	// a "pre-existing remote backup" to protect.
	target := NewZpoolTarget(&TargetConfig{Type: "zpool", Pool: dstPool})
	wp, state := newWorkerPoolForTest(t, target, 0)

	volume := VolumeInfo{
		ID:      "vol",
		LocalID: "vol",
		Name:    "vol",
		Dataset: srcDataset,
	}

	wp.processVolume(volume)

	hist := state.GetHistory(10)
	if len(hist) != 1 {
		t.Fatalf("expected 1 history entry after first send, got %d", len(hist))
	}
	// GetHistory returns most-recent-first, so hist[0] is the latest entry.
	if !hist[0].Success {
		t.Fatalf("expected first send to succeed, got error: %s", hist[0].ErrorMessage)
	}

	// Verify the remote dataset exists and capture its current snapshot list.
	remoteDataset := dstPool + "/vol"
	if out, err := exec.Command("zfs", "list", "-H", remoteDataset).CombinedOutput(); err != nil {
		t.Fatalf("remote dataset missing after first send: %v (%s)", err, out)
	}
	snapsBefore := strings.TrimSpace(runOrFail(t, "zfs", "list", "-H", "-t", "snapshot", "-o", "name", "-r", remoteDataset))

	// Add a bunch more data to source so the next incremental cannot fit.
	fillDataset(t, srcDataset, 48) // pushes well past 64M target

	// Run another cycle — the new send should fail with ErrTargetFull.
	wp.processVolume(volume)

	hist = state.GetHistory(10)
	if len(hist) < 2 {
		t.Fatalf("expected >=2 history entries, got %d", len(hist))
	}
	latest := hist[0]
	if latest.Success {
		t.Fatalf("expected second send to fail, got success")
	}
	if !strings.Contains(strings.ToLower(latest.ErrorMessage), "out of space") &&
		!strings.Contains(latest.ErrorMessage, ErrTargetFull.Error()) {
		t.Errorf("expected out-of-space error, got: %s", latest.ErrorMessage)
	}

	// Verify the remote dataset still exists (Bug 4 protection).
	if out, err := exec.Command("zfs", "list", "-H", remoteDataset).CombinedOutput(); err != nil {
		t.Fatalf("remote dataset destroyed after ENOSPC failure: %v (%s)", err, out)
	}
	snapsAfter := strings.TrimSpace(runOrFail(t, "zfs", "list", "-H", "-t", "snapshot", "-o", "name", "-r", remoteDataset))
	if snapsBefore == "" {
		t.Fatalf("expected pre-existing snapshots before ENOSPC failure, got none")
	}
	if !strings.Contains(snapsAfter, strings.SplitN(snapsBefore, "\n", 2)[0]) {
		t.Errorf("expected pre-ENOSPC snapshot to still exist, before=%q after=%q", snapsBefore, snapsAfter)
	}
}

// blockingTarget implements Target (only Send is meaningful) and blocks Send
// until ctx is done. Used to verify per-volume timeout cancellation.
type blockingTarget struct {
	Target
	sendCalled chan struct{}
}

func (b *blockingTarget) Type() string                                  { return "blocking" }
func (b *blockingTarget) Name() string                                  { return "blocking" }
func (b *blockingTarget) GetAvailableSpace(context.Context) (uint64, error) {
	return 1 << 60, nil
}
func (b *blockingTarget) Send(ctx context.Context, _ SendOptions) error {
	close(b.sendCalled)
	<-ctx.Done()
	return ctx.Err()
}

// TestSendContext_Timeout verifies that sendContext returns a context with
// the configured deadline and that Target.Send observes cancellation when the
// timeout elapses. This is the unit-level guarantee underpinning Bug 5.
func TestSendContext_Timeout(t *testing.T) {
	target := &blockingTarget{sendCalled: make(chan struct{})}
	wp, _ := newWorkerPoolForTest(t, target, 100*time.Millisecond)

	sendCtx, cancel := wp.sendContext(wp.ctx)
	defer cancel()

	deadline, ok := sendCtx.Deadline()
	if !ok {
		t.Fatal("expected sendContext to carry a deadline when volumeTimeout > 0")
	}
	if time.Until(deadline) > 200*time.Millisecond {
		t.Errorf("deadline too far in the future: %v", time.Until(deadline))
	}

	start := time.Now()
	err := target.Send(sendCtx, SendOptions{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected DeadlineExceeded, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Errorf("Send took too long to cancel: %v", elapsed)
	}
}

// TestSendContext_NoTimeout verifies that with volumeTimeout=0 sendContext
// returns a cancellable context without a deadline.
func TestSendContext_NoTimeout(t *testing.T) {
	wp, _ := newWorkerPoolForTest(t, nil, 0)

	sendCtx, cancel := wp.sendContext(wp.ctx)
	defer cancel()

	if _, ok := sendCtx.Deadline(); ok {
		t.Errorf("expected no deadline when volumeTimeout=0")
	}
}
