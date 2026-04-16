package compute

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"exe.dev/exelet/vmm"
	"exe.dev/exelet/vmm/cloudhypervisor"
	api "exe.dev/pkg/api/exe/compute/v1"
)

// fakeVMM satisfies vmm.VMM via embedded nil interface; any method not
// explicitly overridden will panic if called, which is what we want so
// tests fail loudly on unexpected VMM access.
type fakeVMM struct {
	vmm.VMM

	stateFn       func(ctx context.Context, id string) (api.VMState, error)
	liveMigrateFn func(ctx context.Context, id string) (*cloudhypervisor.LiveMigrateLocalResult, error)
}

func (f *fakeVMM) State(ctx context.Context, id string) (api.VMState, error) {
	return f.stateFn(ctx, id)
}

func (f *fakeVMM) LiveMigrateLocal(ctx context.Context, id string) (*cloudhypervisor.LiveMigrateLocalResult, error) {
	return f.liveMigrateFn(ctx, id)
}

// seedRunningInstance writes an instance config to disk and installs a fake
// VMM that reports the instance as running. Returns the fake so tests can
// override behavior further.
func seedRunningInstance(t *testing.T, svc *Service, id string) *fakeVMM {
	t.Helper()
	if err := svc.saveInstanceConfig(&api.Instance{
		ID:       id,
		State:    api.VMState_RUNNING,
		VMConfig: &api.VMConfig{ID: id},
	}); err != nil {
		t.Fatalf("saveInstanceConfig: %v", err)
	}
	f := &fakeVMM{
		stateFn: func(context.Context, string) (api.VMState, error) {
			return api.VMState_RUNNING, nil
		},
	}
	svc.vmm = f
	return f
}

func TestLiveMigrateLocal_EmptyInstanceID(t *testing.T) {
	t.Parallel()
	svc, _ := newTestService(t)

	_, err := svc.LiveMigrateLocal(t.Context(), &api.LiveMigrateLocalRequest{})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got: %v", err)
	}
}

func TestLiveMigrateLocal_NotFound(t *testing.T) {
	t.Parallel()
	svc, _ := newTestService(t)

	_, err := svc.LiveMigrateLocal(t.Context(), &api.LiveMigrateLocalRequest{InstanceID: "does-not-exist"})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("expected NotFound, got: %v", err)
	}
}

func TestLiveMigrateLocal_AlreadyMigrating(t *testing.T) {
	t.Parallel()
	svc, _ := newTestService(t)

	id := "test-instance"
	seedRunningInstance(t, svc, id)

	// Another operation is already migrating this instance.
	if err := svc.lockForMigration(id); err != nil {
		t.Fatalf("lockForMigration: %v", err)
	}
	defer svc.unlockMigration(id)

	_, err := svc.LiveMigrateLocal(t.Context(), &api.LiveMigrateLocalRequest{InstanceID: id})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition, got: %v", err)
	}
}

func TestLiveMigrateLocal_NotRunning(t *testing.T) {
	t.Parallel()
	svc, _ := newTestService(t)

	id := "test-instance"
	f := seedRunningInstance(t, svc, id)
	f.stateFn = func(context.Context, string) (api.VMState, error) {
		return api.VMState_STOPPED, nil
	}

	_, err := svc.LiveMigrateLocal(t.Context(), &api.LiveMigrateLocalRequest{InstanceID: id})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition, got: %v", err)
	}
}

func TestLiveMigrateLocal_HappyPath(t *testing.T) {
	t.Parallel()
	svc, _ := newTestService(t)

	id := "test-instance"
	f := seedRunningInstance(t, svc, id)

	const wantDowntime = 42 * time.Millisecond
	var called int
	f.liveMigrateFn = func(ctx context.Context, gotID string) (*cloudhypervisor.LiveMigrateLocalResult, error) {
		called++
		if gotID != id {
			t.Errorf("LiveMigrateLocal called with id=%q, want %q", gotID, id)
		}
		return &cloudhypervisor.LiveMigrateLocalResult{Downtime: wantDowntime}, nil
	}

	resp, err := svc.LiveMigrateLocal(t.Context(), &api.LiveMigrateLocalRequest{InstanceID: id})
	if err != nil {
		t.Fatalf("LiveMigrateLocal: %v", err)
	}
	if called != 1 {
		t.Fatalf("expected VMM.LiveMigrateLocal called once, got %d", called)
	}
	if resp.Outcome != api.LiveMigrateLocalResponse_LIVE_MIGRATED {
		t.Fatalf("outcome = %v, want LIVE_MIGRATED", resp.Outcome)
	}
	if resp.DowntimeMs != wantDowntime.Milliseconds() {
		t.Fatalf("downtime_ms = %d, want %d", resp.DowntimeMs, wantDowntime.Milliseconds())
	}
	if resp.MigrationError != "" {
		t.Fatalf("migration_error = %q, want empty", resp.MigrationError)
	}

	// Flag should be cleared after the handler returns so subsequent ops
	// don't see ErrMigrating.
	if _, ok := svc.migratingInstances.Load(id); ok {
		t.Fatal("migratingInstances flag still set after successful migration")
	}
}

// TestLiveMigrateLocal_FlagClearedOnVMMError verifies unlockMigration runs
// on the VMM-error path so the instance isn't stuck in a migrating state
// even when cold-restart also fails. (We can't easily test the full
// cold-restart path without standing up real VMM/network scaffolding, but
// we can verify the deferred unlock fires via the error-exit.)
func TestLiveMigrateLocal_FlagClearedOnVMMError(t *testing.T) {
	t.Parallel()
	svc, _ := newTestService(t)

	id := "test-instance"
	f := seedRunningInstance(t, svc, id)
	f.liveMigrateFn = func(context.Context, string) (*cloudhypervisor.LiveMigrateLocalResult, error) {
		return nil, errors.New("ch exploded")
	}

	// Cold-restart path will try to call svc.vmm.Get (via stopInstance),
	// which will panic on our fake. That panic confirms we reached the
	// cold-restart fallback — good enough for this test. Recover from it
	// and then verify the flag is still cleared by the deferred unlock.
	func() {
		defer func() { _ = recover() }()
		_, _ = svc.LiveMigrateLocal(t.Context(), &api.LiveMigrateLocalRequest{InstanceID: id})
	}()

	if _, ok := svc.migratingInstances.Load(id); ok {
		t.Fatal("migratingInstances flag still set after failed migration")
	}
}
