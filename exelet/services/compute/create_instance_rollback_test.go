package compute

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"exe.dev/exelet/services"
)

// orderTrackingPreparer records when ReleaseVMCgroup is called, so tests can
// assert ordering relative to other rollback steps.
type orderTrackingPreparer struct {
	releaseSeq    atomic.Int64
	instanceDir   string
	dirGoneAtCall atomic.Bool
}

func (p *orderTrackingPreparer) PrepareVMCgroup(_ context.Context, _, _ string) (string, error) {
	return "", nil
}

func (p *orderTrackingPreparer) ReleaseVMCgroup(_ context.Context, _, _ string) error {
	p.releaseSeq.Store(1)
	if _, err := os.Stat(p.instanceDir); os.IsNotExist(err) {
		p.dirGoneAtCall.Store(true)
	}
	return nil
}

// TestRollback_RemovesInstanceDirBeforeReleasingCgroup ensures the rollback
// path nukes the on-disk instance config before calling ReleaseVMCgroup. This
// ordering matters because the resource manager poll loop reads instance
// configs and, on running VMs with a cached PID, calls applyPriority ->
// ensureCgroup, which would otherwise re-create the very scope rollback is
// trying to drop. Removing the config first turns the poll cycle into a
// no-op for this id.
func TestRollback_RemovesInstanceDirBeforeReleasingCgroup(t *testing.T) {
	tmp := t.TempDir()
	instanceDir := filepath.Join(tmp, "vm-test")
	if err := os.MkdirAll(instanceDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	prep := &orderTrackingPreparer{instanceDir: instanceDir}
	rb := &createInstanceRollback{
		ctx:                context.Background(),
		log:                slog.New(slog.NewTextHandler(os.Stderr, nil)),
		serviceContext:     &services.ServiceContext{CgroupPreparer: prep},
		instanceID:         "test",
		instanceDir:        instanceDir,
		instanceDirCreated: true,
	}

	rb.Rollback()

	if prep.releaseSeq.Load() == 0 {
		t.Fatalf("ReleaseVMCgroup was not called")
	}
	if !prep.dirGoneAtCall.Load() {
		t.Fatalf("instance directory still existed when ReleaseVMCgroup ran; ordering is wrong (race window for poll-loop re-create)")
	}
	if _, err := os.Stat(instanceDir); !os.IsNotExist(err) {
		t.Fatalf("instance directory should be removed after rollback, stat err = %v", err)
	}
}
