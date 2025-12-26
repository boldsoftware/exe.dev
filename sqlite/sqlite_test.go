package sqlite

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	_ "modernc.org/sqlite"
)

func TestWrapErr(t *testing.T) {
	err := wrapErr("prefix", nil)
	if err != nil {
		t.Fatalf("err=%v, want nil", err)
	}

	func() {
		// wrapErr skips the function calling it,
		// so the anonymous function is skipped over.
		err = wrapErr("prefix", errors.New("testerr"))
		if err == nil {
			t.Fatal("err=nil, want an error")
		}
	}()
	got := err.Error()
	const want = "sqlite.TestWrapErr: prefix: testerr"
	if got != want {
		t.Errorf("err=%q, want %q", got, want)
	}
}

func TestPool(t *testing.T) {
	p, err := New(filepath.Join(t.TempDir(), "testpool.sqlite"), 2)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	err = p.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		if _, err := tx.Exec("CREATE TABLE t (c);"); err != nil {
			return err
		}
		_, err := tx.Exec("INSERT INTO t (c) VALUES (1);")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	var count, count2 int
	err = p.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		// Use a background context here directly to work around any
		// context checking we do to stop nested transactions.
		// We just want to demonstrate two read transactions can be open
		// simultaneously.
		err = p.Rx(context.Background(), func(ctx context.Context, rx *Rx) error {
			return rx.QueryRow("SELECT count(*) FROM t;").Scan(&count2)
		})
		if err != nil {
			return fmt.Errorf("rx2: %w", err)
		}
		return rx.QueryRow("SELECT count(*) FROM t;").Scan(&count)
	})
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("count=%d, want 1", count)
	}
	if count2 != 1 {
		t.Fatalf("count2=%d, want 1", count)
	}

	err = p.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		_, err := tx.Exec("INSERT INTO t (c) VALUES (1);")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	wantErr := fmt.Errorf("we want this error")
	err = p.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		_, err := tx.Exec("INSERT INTO t (c) VALUES (1);")
		if err != nil {
			return err
		}
		return wantErr // rollback
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("err=%v, want wantErr", err)
	}

	err = p.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		return rx.QueryRow("SELECT count(*) FROM t;").Scan(&count)
	})
	if err != nil {
		t.Fatal(err)
	}

	if count != 2 {
		t.Fatalf("count=%d, want 2", count)
	}

	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestNestPanic(t *testing.T) {
	p, err := New(filepath.Join(t.TempDir(), "testpool.sqlite"), 2)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	ctx := context.Background()

	t.Run("rx-inside-rx", func(t *testing.T) {
		defer func() {
			r := recover()
			if r == nil {
				t.Fatal("expecting nested Rx panic, got none")
			}
			if want := "Rx inside Rx (sqlite.TestNestPanic.func1)"; r != want {
				t.Fatalf("panic=%q, want %q", r, want)
			}
		}()
		err := p.Rx(ctx, func(ctx context.Context, rx *Rx) error {
			return p.Rx(ctx, func(ctx context.Context, rx *Rx) error {
				return nil
			})
		})
		if err != nil {
			t.Fatal(err)
		}
	})

	t.Run("rx-inside-tx", func(t *testing.T) {
		defer func() {
			r := recover()
			if r == nil {
				t.Fatal("expecting nested Rx panic, got none")
			}
			if want := "Rx inside Tx (sqlite.TestNestPanic.func2)"; r != want {
				t.Fatalf("panic=%q, want %q", r, want)
			}
		}()
		err := p.Tx(ctx, func(ctx context.Context, tx *Tx) error {
			return p.Rx(ctx, func(ctx context.Context, rx *Rx) error {
				return nil
			})
		})
		if err != nil {
			t.Fatal(err)
		}
	})

	t.Run("tx-inside-tx", func(t *testing.T) {
		defer func() {
			r := recover()
			if r == nil {
				t.Fatal("expecting nested Rx panic, got none")
			}
			if want := "Tx inside Tx (sqlite.TestNestPanic.func3)"; r != want {
				t.Fatalf("panic=%q, want %q", r, want)
			}
		}()
		err := p.Tx(ctx, func(ctx context.Context, tx *Tx) error {
			return p.Tx(ctx, func(ctx context.Context, tx *Tx) error {
				return nil
			})
		})
		if err != nil {
			t.Fatal(err)
		}
	})

	t.Run("tx-inside-rx", func(t *testing.T) {
		defer func() {
			r := recover()
			if r == nil {
				t.Fatal("expecting nested Rx panic, got none")
			}
			if want := "Tx inside Rx (sqlite.TestNestPanic.func4)"; r != want {
				t.Fatalf("panic=%q, want %q", r, want)
			}
		}()
		err := p.Rx(ctx, func(ctx context.Context, rx *Rx) error {
			return p.Tx(ctx, func(ctx context.Context, tx *Tx) error {
				return nil
			})
		})
		if err != nil {
			t.Fatal(err)
		}
	})
}

func TestExpiredContextRollback(t *testing.T) {
	// Set up the DB.
	p, err := New(filepath.Join(t.TempDir(), "testpool.sqlite"), 1)
	if err != nil {
		t.Fatal(err)
	}
	bg := context.Background()
	err = p.Tx(bg, func(ctx context.Context, tx *Tx) error {
		_, err := tx.Exec("CREATE TABLE t (c);")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	// Cancel a context mid-way through an Rx.
	ctx, cancel := context.WithCancel(bg)
	err = p.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		cancel()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// Ensure that a subsequent transaction succeeds.
	var count int
	err = p.Rx(bg, func(ctx context.Context, rx *Rx) error {
		return rx.QueryRow("SELECT count(*) FROM t;").Scan(&count)
	})
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("count=%d, want 0", count)
	}

	// Do the same for a Tx.
	ctx, cancel = context.WithCancel(bg)
	fmt.Println("cancel")
	err = p.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		cancel()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// Ensure that a subsequent transaction succeeds.
	err = p.Tx(bg, func(ctx context.Context, tx *Tx) error {
		return tx.QueryRow("SELECT count(*) FROM t;").Scan(&count)
	})
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("count=%d, want 0", count)
	}
}

func TestExecWithoutTx(t *testing.T) {
	p, err := New(filepath.Join(t.TempDir(), "testexecwithouttx.sqlite"), 2)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := p.Exec(ctx, "PRAGMA wal_checkpoint(TRUNCATE);"); err != nil {
		t.Fatal(err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestTxLeak(t *testing.T) {
	p, err := New(filepath.Join(t.TempDir(), "testtxleak.sqlite"), 2)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5000; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		go cancel()
		err := p.Tx(ctx, func(ctx context.Context, tx *Tx) error { return nil })
		if err != nil && strings.Contains(err.Error(), "LEAK") {
			t.Error(err)
		}
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRxLeak(t *testing.T) {
	p, err := New(filepath.Join(t.TempDir(), "testtxleak.sqlite"), 2)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5000; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		go cancel()
		err := p.Tx(ctx, func(ctx context.Context, tx *Tx) error { return nil })
		if err != nil && strings.Contains(err.Error(), "LEAK") {
			t.Error(err)
		}

	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestLeakCounters(t *testing.T) {
	// Create a custom registry for testing
	testRegistry := prometheus.NewRegistry()
	RegisterSQLiteMetrics(testRegistry)

	// Gather metrics to verify leak counters are registered
	metricFamilies, err := testRegistry.Gather()
	if err != nil {
		t.Fatal(err)
	}

	// Verify we have the leak counter metrics
	expectedMetrics := []string{
		"sqlite_tx_leaks_total",
		"sqlite_rx_leaks_total",
	}

	foundMetrics := make(map[string]bool)
	for _, mf := range metricFamilies {
		foundMetrics[mf.GetName()] = true
	}

	for _, expected := range expectedMetrics {
		if !foundMetrics[expected] {
			t.Errorf("Expected leak metric %s not found", expected)
		}
	}

	t.Logf("Successfully found leak counter metrics")
}

func TestLatencyHistograms(t *testing.T) {
	// Create a custom registry for testing
	testRegistry := prometheus.NewRegistry()
	RegisterSQLiteMetrics(testRegistry)

	// Gather metrics to verify leak counters are registered
	metricFamilies, err := testRegistry.Gather()
	if err != nil {
		t.Fatal(err)
	}

	// Verify we have the latency histogram metrics
	expectedMetrics := []string{
		"sqlite_rx_latency",
		"sqlite_tx_latency",
	}

	foundMetrics := make(map[string]bool)
	for _, mf := range metricFamilies {
		foundMetrics[mf.GetName()] = true
	}

	for _, expected := range expectedMetrics {
		if !foundMetrics[expected] {
			t.Errorf("Expected latency metric %s not found", expected)
		}
	}
}

func TestSQLiteErrorPropagation(t *testing.T) {
	p, err := New(filepath.Join(t.TempDir(), "testerrors.sqlite"), 2)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	ctx := context.Background()

	// Set up a table with constraints to trigger errors
	err = p.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		_, err := tx.Exec(`CREATE TABLE test_errors (
			id INTEGER PRIMARY KEY,
			name TEXT NOT NULL UNIQUE,
			value INTEGER CHECK(value > 0)
		);`)
		if err != nil {
			return err
		}
		_, err = tx.Exec(`INSERT INTO test_errors (id, name, value) VALUES (1, 'first', 10);`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	// The modernc.org/sqlite driver reports errors with human-readable messages
	// and SQLite error codes in parentheses. We verify:
	// 1. The descriptive error message is present
	// 2. The SQLite error code is included (in parentheses)

	t.Run("write_unique_constraint", func(t *testing.T) {
		err := p.Tx(ctx, func(ctx context.Context, tx *Tx) error {
			_, err := tx.Exec(`INSERT INTO test_errors (id, name, value) VALUES (2, 'first', 20);`)
			return err
		})
		if err == nil {
			t.Fatal("expected UNIQUE constraint error, got nil")
		}
		errStr := err.Error()
		if !strings.Contains(errStr, "UNIQUE constraint failed") {
			t.Errorf("error should contain 'UNIQUE constraint failed', got: %v", err)
		}
		// Error code 2067 = SQLITE_CONSTRAINT_UNIQUE
		if !strings.Contains(errStr, "(2067)") {
			t.Errorf("error should contain SQLite error code (2067), got: %v", err)
		}
	})

	t.Run("write_not_null_constraint", func(t *testing.T) {
		err := p.Tx(ctx, func(ctx context.Context, tx *Tx) error {
			_, err := tx.Exec(`INSERT INTO test_errors (id, name, value) VALUES (3, NULL, 30);`)
			return err
		})
		if err == nil {
			t.Fatal("expected NOT NULL constraint error, got nil")
		}
		errStr := err.Error()
		if !strings.Contains(errStr, "NOT NULL constraint failed") {
			t.Errorf("error should contain 'NOT NULL constraint failed', got: %v", err)
		}
		// Error code 1299 = SQLITE_CONSTRAINT_NOTNULL
		if !strings.Contains(errStr, "(1299)") {
			t.Errorf("error should contain SQLite error code (1299), got: %v", err)
		}
	})

	t.Run("write_check_constraint", func(t *testing.T) {
		err := p.Tx(ctx, func(ctx context.Context, tx *Tx) error {
			_, err := tx.Exec(`INSERT INTO test_errors (id, name, value) VALUES (4, 'fourth', -5);`)
			return err
		})
		if err == nil {
			t.Fatal("expected CHECK constraint error, got nil")
		}
		errStr := err.Error()
		if !strings.Contains(errStr, "CHECK constraint failed") {
			t.Errorf("error should contain 'CHECK constraint failed', got: %v", err)
		}
		// Error code 275 = SQLITE_CONSTRAINT_CHECK
		if !strings.Contains(errStr, "(275)") {
			t.Errorf("error should contain SQLite error code (275), got: %v", err)
		}
	})

	t.Run("write_no_such_table", func(t *testing.T) {
		err := p.Tx(ctx, func(ctx context.Context, tx *Tx) error {
			_, err := tx.Exec(`INSERT INTO nonexistent_table (col) VALUES (1);`)
			return err
		})
		if err == nil {
			t.Fatal("expected no such table error, got nil")
		}
		errStr := err.Error()
		if !strings.Contains(errStr, "no such table") {
			t.Errorf("error should contain 'no such table', got: %v", err)
		}
		// Error code 1 = SQLITE_ERROR
		if !strings.Contains(errStr, "(1)") {
			t.Errorf("error should contain SQLite error code (1), got: %v", err)
		}
	})

	t.Run("read_no_such_table", func(t *testing.T) {
		err := p.Rx(ctx, func(ctx context.Context, rx *Rx) error {
			var count int
			return rx.QueryRow(`SELECT count(*) FROM nonexistent_table;`).Scan(&count)
		})
		if err == nil {
			t.Fatal("expected no such table error, got nil")
		}
		errStr := err.Error()
		if !strings.Contains(errStr, "no such table") {
			t.Errorf("error should contain 'no such table', got: %v", err)
		}
		// Error code 1 = SQLITE_ERROR
		if !strings.Contains(errStr, "(1)") {
			t.Errorf("error should contain SQLite error code (1), got: %v", err)
		}
	})

	t.Run("read_syntax_error", func(t *testing.T) {
		err := p.Rx(ctx, func(ctx context.Context, rx *Rx) error {
			var count int
			return rx.QueryRow(`SELEC * FROM test_errors;`).Scan(&count)
		})
		if err == nil {
			t.Fatal("expected syntax error, got nil")
		}
		errStr := err.Error()
		if !strings.Contains(errStr, "syntax error") {
			t.Errorf("error should contain 'syntax error', got: %v", err)
		}
		// Error code 1 = SQLITE_ERROR
		if !strings.Contains(errStr, "(1)") {
			t.Errorf("error should contain SQLite error code (1), got: %v", err)
		}
	})

	t.Run("read_query_rows_error", func(t *testing.T) {
		err := p.Rx(ctx, func(ctx context.Context, rx *Rx) error {
			rows, err := rx.Query(`SELECT * FROM nonexistent_table;`)
			if err != nil {
				return err
			}
			defer rows.Close()
			return nil
		})
		if err == nil {
			t.Fatal("expected no such table error, got nil")
		}
		errStr := err.Error()
		if !strings.Contains(errStr, "no such table") {
			t.Errorf("error should contain 'no such table', got: %v", err)
		}
		// Error code 1 = SQLITE_ERROR
		if !strings.Contains(errStr, "(1)") {
			t.Errorf("error should contain SQLite error code (1), got: %v", err)
		}
	})
}
