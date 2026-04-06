package compute

import (
	"context"
	"sync"
	"testing"
	"time"

	api "exe.dev/pkg/api/exe/compute/v1"
)

// TestMigrationBlocksLifecycleOps verifies that once lockForMigration succeeds,
// concurrent lifecycle operations (which take lockInstance then checkNotMigrating)
// observe the migration flag and fail with ErrMigrating.
func TestMigrationBlocksLifecycleOps(t *testing.T) {
	t.Parallel()
	svc, _ := newTestService(t)

	id := "test-instance"

	// Set migration flag (acquires and releases lockInstance internally)
	if err := svc.lockForMigration(id); err != nil {
		t.Fatalf("lockForMigration failed: %v", err)
	}
	defer svc.unlockMigration(id)

	// Lifecycle ops should now fail with ErrMigrating
	unlock := svc.lockInstance(id)
	err := svc.checkNotMigrating(id)
	unlock()

	if err != api.ErrMigrating {
		t.Fatalf("expected ErrMigrating, got: %v", err)
	}
}

// TestLifecycleInFlightBlocksMigration verifies that lockForMigration waits
// for an in-flight lifecycle operation to complete before setting the flag.
// This ensures no TOCTOU gap between the lifecycle check and migration start.
func TestLifecycleInFlightBlocksMigration(t *testing.T) {
	t.Parallel()
	svc, _ := newTestService(t)

	id := "test-instance"

	// Lifecycle op holds the lock
	unlock := svc.lockInstance(id)

	migrationDone := make(chan error, 1)
	migrationStarted := make(chan struct{})

	go func() {
		close(migrationStarted)
		// lockForMigration must wait for the lifecycle lock to be released
		migrationDone <- svc.lockForMigration(id)
	}()

	<-migrationStarted

	// Release lifecycle lock — migration can now proceed
	unlock()

	if err := <-migrationDone; err != nil {
		t.Fatalf("lockForMigration failed: %v", err)
	}
	svc.unlockMigration(id)
}

// TestConcurrentMigrationAndLifecycle hammers lockForMigration and lifecycle
// lock+check concurrently to verify no races (run with -race).
func TestConcurrentMigrationAndLifecycle(t *testing.T) {
	t.Parallel()
	svc, _ := newTestService(t)

	id := "test-instance"
	const goroutines = 50
	const iterations = 100

	var wg sync.WaitGroup

	// Half the goroutines simulate migration lock/unlock cycles
	for range goroutines / 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range iterations {
				if err := svc.lockForMigration(id); err != nil {
					// Another goroutine holds migration lock — expected
					continue
				}
				svc.unlockMigration(id)
			}
		}()
	}

	// Other half simulate lifecycle lock+check cycles
	for range goroutines / 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range iterations {
				unlock := svc.lockInstance(id)
				_ = svc.checkNotMigrating(id) // may or may not be migrating
				unlock()
			}
		}()
	}

	wg.Wait()
}

// TestMigrationUnlockAllowsLifecycle verifies that after unlockMigration,
// lifecycle operations succeed again.
func TestMigrationUnlockAllowsLifecycle(t *testing.T) {
	t.Parallel()
	svc, _ := newTestService(t)

	id := "test-instance"

	// Lock then unlock migration
	if err := svc.lockForMigration(id); err != nil {
		t.Fatalf("lockForMigration failed: %v", err)
	}
	svc.unlockMigration(id)

	// Lifecycle should now succeed
	unlock := svc.lockInstance(id)
	err := svc.checkNotMigrating(id)
	unlock()

	if err != nil {
		t.Fatalf("expected nil after migration unlock, got: %v", err)
	}
}

// TestDoubleMigrationFails verifies that a second lockForMigration for the
// same instance returns ErrMigrating.
func TestDoubleMigrationFails(t *testing.T) {
	t.Parallel()
	svc, _ := newTestService(t)

	id := "test-instance"

	if err := svc.lockForMigration(id); err != nil {
		t.Fatalf("first lockForMigration failed: %v", err)
	}
	defer svc.unlockMigration(id)

	if err := svc.lockForMigration(id); err != api.ErrMigrating {
		t.Fatalf("expected ErrMigrating on double lock, got: %v", err)
	}
}

// TestShutdownDrainCancelsPendingMigrations verifies that Stop() cancels
// migrations waiting for a semaphore slot and rejects new migrations, but
// allows in-flight migrations (already past the semaphore) to complete.
func TestShutdownDrainCancelsPendingMigrations(t *testing.T) {
	t.Parallel()
	svc, _ := newTestService(t)
	svc.tierMigrationCtx, svc.tierMigrationCancel = context.WithCancel(context.Background())

	// Fill the semaphore so subsequent migrations block waiting for a slot.
	for range cap(svc.tierMigrationSem) {
		svc.tierMigrationSem <- struct{}{}
	}

	// Simulate an in-flight migration goroutine that already acquired a slot.
	svc.tierMigrationWg.Add(1)
	inFlightDone := make(chan struct{})
	go func() {
		defer svc.tierMigrationWg.Done()
		// Simulate work — wait until the test signals completion.
		<-inFlightDone
	}()

	// Launch a "pending" goroutine that tries to acquire a semaphore slot.
	// It should observe tierMigrationCtx cancellation and exit.
	pendingExited := make(chan struct{})
	svc.tierMigrationWg.Add(1)
	go func() {
		defer svc.tierMigrationWg.Done()
		defer close(pendingExited)
		select {
		case svc.tierMigrationSem <- struct{}{}:
			// Should not happen — semaphore is full.
			t.Error("unexpectedly acquired semaphore slot")
			<-svc.tierMigrationSem
		case <-svc.tierMigrationCtx.Done():
			// Expected: shutdown cancelled us.
		}
	}()

	// Trigger shutdown — this cancels tierMigrationCtx.
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopCancel()

	stopDone := make(chan error, 1)
	go func() {
		stopDone <- svc.Stop(stopCtx)
	}()

	// The pending goroutine should exit promptly after shutdown signal.
	select {
	case <-pendingExited:
	case <-time.After(2 * time.Second):
		t.Fatal("pending migration did not exit after shutdown")
	}

	// Stop should still be waiting for the in-flight goroutine.
	select {
	case <-stopDone:
		t.Fatal("Stop returned before in-flight migration finished")
	case <-time.After(100 * time.Millisecond):
		// Good — still draining.
	}

	// New migrations should be rejected.
	if svc.tierMigrationCtx.Err() == nil {
		t.Fatal("expected tierMigrationCtx to be cancelled after Stop")
	}

	// Let the in-flight migration complete.
	close(inFlightDone)

	// Now Stop should return.
	select {
	case err := <-stopDone:
		if err != nil {
			t.Fatalf("Stop returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not return after in-flight migration finished")
	}

	// Drain the semaphore slots we filled.
	for range cap(svc.tierMigrationSem) {
		<-svc.tierMigrationSem
	}
}
