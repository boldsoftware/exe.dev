package compute

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

type fakeReleasePreparer struct {
	calls         int
	lastID        string
	lastGid       string
	dirGoneAtCall bool
	instanceDir   string
	err           error
}

func (p *fakeReleasePreparer) ReleaseVMCgroup(_ context.Context, id, gid string) error {
	p.calls++
	p.lastID = id
	p.lastGid = gid
	if p.instanceDir != "" {
		if _, err := os.Stat(p.instanceDir); os.IsNotExist(err) {
			p.dirGoneAtCall = true
		}
	}
	return p.err
}

type fakeRollbackStorage struct{}

func (fakeRollbackStorage) Delete(_ context.Context, _ string) error { return nil }

type fakeRollbackNetwork struct{}

func (fakeRollbackNetwork) DeleteInterface(_ context.Context, _, _, _ string) error { return nil }

func newReceiveRollback(t *testing.T, prep *fakeReleasePreparer) (*receiveVMRollback, string) {
	t.Helper()
	tmp := t.TempDir()
	dir := filepath.Join(tmp, "vm-receive-test")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if prep != nil {
		prep.instanceDir = dir
	}
	rb := &receiveVMRollback{
		ctx:                context.Background(),
		log:                slog.New(slog.NewTextHandler(os.Stderr, nil)),
		storageManager:     fakeRollbackStorage{},
		networkManager:     fakeRollbackNetwork{},
		cgroupPreparer:     prep,
		instanceID:         "vm-receive-test",
		instanceDir:        dir,
		groupID:            "acct-7",
		instanceDirCreated: true,
	}
	return rb, dir
}

// TestReceiveVMRollback_ReleasesCgroupAfterRestore covers the
// restore-success-then-later-step-fail case: finalizeLiveReceive sets
// rb.cgroupCreated=true (because RestoreFromSnapshot has invoked
// PrepareVMCgroup), then a later step (e.g. proxy setup, AbortReceiveVM)
// triggers Rollback. The cgroup scope must be released or it leaks forever.
func TestReceiveVMRollback_ReleasesCgroupAfterRestore(t *testing.T) {
	prep := &fakeReleasePreparer{}
	rb, dir := newReceiveRollback(t, prep)
	rb.cgroupCreated = true

	rb.Rollback()

	if prep.calls != 1 {
		t.Fatalf("ReleaseVMCgroup call count = %d, want 1", prep.calls)
	}
	if prep.lastID != "vm-receive-test" || prep.lastGid != "acct-7" {
		t.Fatalf("ReleaseVMCgroup got id=%q gid=%q, want vm-receive-test/acct-7", prep.lastID, prep.lastGid)
	}
	if !prep.dirGoneAtCall {
		t.Fatalf("instance dir still existed when ReleaseVMCgroup ran; ordering races the resource manager poll loop")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("instance dir should be removed, stat err = %v", err)
	}
}

// TestReceiveVMRollback_ReleasesCgroupAfterColdBootFallback covers the
// fallback path: RestoreFromSnapshot fails, finalizeLiveReceive cold-boots
// from the source disk (which also goes through startCHProcess and creates
// the scope), then a later step fails. The scope still needs releasing.
//
// Implementation note: cgroupCreated is set BEFORE the RestoreFromSnapshot
// call in finalizeLiveReceive, so it is true on both branches. This test
// just exercises the same flag-set-true contract.
func TestReceiveVMRollback_ReleasesCgroupAfterColdBootFallback(t *testing.T) {
	prep := &fakeReleasePreparer{}
	rb, _ := newReceiveRollback(t, prep)
	rb.cgroupCreated = true // set unconditionally by finalizeLiveReceive before restore attempt

	rb.Rollback()

	if prep.calls != 1 {
		t.Fatalf("ReleaseVMCgroup call count = %d, want 1 (cold-boot fallback path)", prep.calls)
	}
}

// TestReceiveVMRollback_NoReleaseWhenScopeNeverCreated guards against
// over-release: if the receive failed before finalizeLiveReceive ran (e.g.
// during data transfer), no scope was created, so calling ReleaseVMCgroup
// would just be wasted work — and on shared-preparer setups could log
// spurious "already gone" warnings.
func TestReceiveVMRollback_NoReleaseWhenScopeNeverCreated(t *testing.T) {
	prep := &fakeReleasePreparer{}
	rb, _ := newReceiveRollback(t, prep)
	// cgroupCreated stays false — represents an early failure

	rb.Rollback()

	if prep.calls != 0 {
		t.Fatalf("ReleaseVMCgroup unexpectedly called %d time(s) before scope was ever created", prep.calls)
	}
}

// TestReceiveVMRollback_TolerantOfPreparerError verifies a failing
// ReleaseVMCgroup does not panic or block other rollback steps.
func TestReceiveVMRollback_TolerantOfPreparerError(t *testing.T) {
	prep := &fakeReleasePreparer{err: errors.New("boom")}
	rb, dir := newReceiveRollback(t, prep)
	rb.cgroupCreated = true

	rb.Rollback()

	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("instance dir should still be removed even when preparer errors, stat err = %v", err)
	}
}
