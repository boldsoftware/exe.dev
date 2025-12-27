package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestCancelDuringTransaction verifies that cancelling a context during
// a transaction doesn't leak or corrupt connections in the pool.
func TestCancelDuringTransaction(t *testing.T) {
	p, err := New(filepath.Join(t.TempDir(), "cancel.sqlite"), 2)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	// Use a timeout so the test fails fast if connections leak and exhaust the pool.
	testCtx, testCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer testCancel()

	var leaks atomic.Int32
	var corrupted atomic.Int32
	var wg sync.WaitGroup

	// Spawn workers that cancel at various times
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(delay time.Duration) {
			defer wg.Done()
			for j := 0; j < 500; j++ {
				if testCtx.Err() != nil {
					return // test timed out
				}
				ctx, cancel := context.WithCancel(testCtx)
				go func() {
					time.Sleep(delay)
					cancel()
				}()

				err := p.Rx(ctx, func(ctx context.Context, rx *Rx) error {
					var n int
					return rx.QueryRow("SELECT 1").Scan(&n)
				})
				if err != nil && strings.Contains(err.Error(), "LEAK") {
					leaks.Add(1)
				}

				// Periodically verify pool health
				if j%100 == 0 {
					healthCtx, healthCancel := context.WithTimeout(testCtx, 5*time.Second)
					err := p.Rx(healthCtx, func(ctx context.Context, rx *Rx) error {
						var n int
						return rx.QueryRow("SELECT 1").Scan(&n)
					})
					healthCancel()
					if err != nil && strings.Contains(err.Error(), "closed") {
						corrupted.Add(1)
					}
				}
			}
		}(time.Duration(i) * time.Microsecond)
	}

	wg.Wait()

	if testCtx.Err() != nil {
		t.Fatal("test timed out - likely connection leak caused pool exhaustion")
	}

	if n := leaks.Load(); n > 0 {
		t.Errorf("detected %d leaks", n)
	}
	if n := corrupted.Load(); n > 0 {
		t.Errorf("pool corrupted %d times (closed connection returned to pool)", n)
	}
}

// TestCommitFailureErrorWrapping verifies that COMMIT errors are wrapped with context.
func TestCommitFailureErrorWrapping(t *testing.T) {
	p, err := New(filepath.Join(t.TempDir(), "commit_err.sqlite"), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	ctx := context.Background()

	// Set up a table
	err = p.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		_, err := tx.Exec("CREATE TABLE t (c INTEGER);")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	// Use a custom error to verify error propagation from the user function.
	userErr := errors.New("user error")
	err = p.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		_, err := tx.Exec("INSERT INTO t (c) VALUES (1);")
		if err != nil {
			return err
		}
		return userErr
	})
	// User error should be returned directly (not wrapped as commit error).
	if !errors.Is(err, userErr) {
		t.Fatalf("expected userErr, got: %v", err)
	}

	// Connection should still be usable after rollback.
	var count int
	err = p.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		return rx.QueryRow("SELECT count(*) FROM t;").Scan(&count)
	})
	if err != nil {
		t.Fatalf("pool unusable after user error: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected rollback, got count=%d", count)
	}
}

// TestCommitFailurePoolHealth verifies that after a commit failure,
// the connection is properly handled and the pool remains healthy.
func TestCommitFailurePoolHealth(t *testing.T) {
	p, err := New(filepath.Join(t.TempDir(), "commit_pool.sqlite"), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	ctx := context.Background()

	// Set up a table
	err = p.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		_, err := tx.Exec("CREATE TABLE t (c INTEGER);")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	// Run many transactions with user errors to stress the rollback path.
	for i := 0; i < 100; i++ {
		err = p.Tx(ctx, func(ctx context.Context, tx *Tx) error {
			_, err := tx.Exec("INSERT INTO t (c) VALUES (?);", i)
			if err != nil {
				return err
			}
			return errors.New("intentional rollback")
		})
		if err == nil {
			t.Fatal("expected error")
		}
	}

	// Verify the pool is still healthy.
	var count int
	err = p.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		return tx.QueryRow("SELECT count(*) FROM t;").Scan(&count)
	})
	if err != nil {
		t.Fatalf("pool unusable after stress test: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected all rollbacks, got count=%d", count)
	}
}

// TestCancelDuringCommit verifies that cancelling the context during or just before
// COMMIT doesn't leak connections.
func TestCancelDuringCommit(t *testing.T) {
	p, err := New(filepath.Join(t.TempDir(), "cancel_commit.sqlite"), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	bg := context.Background()

	// Set up a table
	err = p.Tx(bg, func(ctx context.Context, tx *Tx) error {
		_, err := tx.Exec("CREATE TABLE t (c INTEGER);")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	// Run many transactions where we cancel context at the end of the user func.
	// This tests cancellation around the time COMMIT is called.
	for i := 0; i < 100; i++ {
		ctx, cancel := context.WithCancel(bg)
		err = p.Tx(ctx, func(ctx context.Context, tx *Tx) error {
			_, err := tx.Exec("INSERT INTO t (c) VALUES (?);", i)
			if err != nil {
				return err
			}
			cancel() // Cancel right before commit
			return nil
		})
		// Error is acceptable (context cancelled), but no LEAK errors.
		if err != nil && strings.Contains(err.Error(), "LEAK") {
			t.Fatalf("iteration %d: connection leaked: %v", i, err)
		}
	}

	// Verify the pool is still healthy by running a successful transaction.
	err = p.Tx(bg, func(ctx context.Context, tx *Tx) error {
		_, err := tx.Exec("INSERT INTO t (c) VALUES (999);")
		return err
	})
	if err != nil {
		t.Fatalf("pool unusable after cancel stress test: %v", err)
	}

	// At least one transaction should have succeeded (the final one).
	var count int
	err = p.Rx(bg, func(ctx context.Context, rx *Rx) error {
		return rx.QueryRow("SELECT count(*) FROM t WHERE c = 999;").Scan(&count)
	})
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected final insert to succeed, got count=%d", count)
	}
}
