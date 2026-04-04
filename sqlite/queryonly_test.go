package sqlite

import (
	"context"
	"path/filepath"
	"testing"
)

// TestQueryOnlyOverridePersists verifies that a PRAGMA query_only=0 issued
// within an Rx persists on the connection after the Rx completes, unless
// the caller explicitly resets it. This documents the threat model: callers
// that accept arbitrary SQL (like a debug endpoint) must clean up after
// themselves.
func TestQueryOnlyOverridePersists(t *testing.T) {
	p, err := New(filepath.Join(t.TempDir(), "queryonly.sqlite"), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	ctx := context.Background()

	err = p.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		_, err := tx.Exec("CREATE TABLE t (c TEXT);")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	// Override the pragma without cleaning up.
	err = p.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		rows, err := rx.Query("PRAGMA query_only=0;")
		if err != nil {
			return err
		}
		rows.Close()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// Without cleanup, the next Rx inherits query_only=0.
	err = p.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		var val int
		if err := rx.QueryRow("PRAGMA query_only;").Scan(&val); err != nil {
			return err
		}
		if val != 0 {
			t.Fatalf("expected query_only=0 (contaminated), got %d", val)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// Now simulate the debug endpoint pattern: override + deferred cleanup.
	err = p.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		defer func() {
			if rows, err := rx.Query("PRAGMA query_only=1;"); err == nil {
				rows.Close()
			}
		}()
		rows, err := rx.Query("PRAGMA query_only=0;")
		if err != nil {
			return err
		}
		rows.Close()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// After cleanup, the next Rx sees query_only=1.
	err = p.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		var val int
		if err := rx.QueryRow("PRAGMA query_only;").Scan(&val); err != nil {
			return err
		}
		if val != 1 {
			t.Errorf("expected query_only=1 after cleanup, got %d", val)
		}

		// Verify writes are blocked.
		rows, err := rx.Query("INSERT INTO t (c) VALUES ('should-fail');")
		if err == nil {
			rows.Close()
			t.Error("INSERT succeeded — query_only not enforced")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
