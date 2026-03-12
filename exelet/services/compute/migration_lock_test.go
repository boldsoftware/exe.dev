package compute

import (
	"sync"
	"testing"

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
