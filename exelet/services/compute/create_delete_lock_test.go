package compute

import (
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	api "exe.dev/pkg/api/exe/compute/v1"
)

// TestDeleteBlocksWhileInstanceLockHeld verifies that DeleteInstance blocks on
// the per-instance lock. Since createInstance now acquires lockInstance(id) for
// the duration of setup, a DeleteInstance arriving mid-creation will wait
// instead of racing against network/storage/config writes.
//
// We simulate an in-flight create by holding the lock directly, issue a
// DeleteInstance for the same id, and confirm it does not return until the
// lock is released. (The instance does not exist, so Delete returns NotFound
// once unblocked — that's fine; what matters is the blocking behavior.)
func TestDeleteBlocksWhileInstanceLockHeld(t *testing.T) {
	t.Parallel()
	svc, _ := newTestService(t)

	id := "test-instance"

	unlock := svc.lockInstance(id)

	deleteDone := make(chan error, 1)
	go func() {
		_, err := svc.DeleteInstance(t.Context(), &api.DeleteInstanceRequest{ID: id})
		deleteDone <- err
	}()

	select {
	case err := <-deleteDone:
		t.Fatalf("DeleteInstance returned before lock released: err=%v", err)
	case <-time.After(100 * time.Millisecond):
		// Good — Delete is blocked on lockInstance.
	}

	unlock()

	select {
	case err := <-deleteDone:
		// Instance doesn't exist; expect NotFound once unblocked.
		if s, ok := status.FromError(err); !ok || s.Code() != codes.NotFound {
			t.Fatalf("expected NotFound after unblock, got: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("DeleteInstance did not return after lock release")
	}
}
