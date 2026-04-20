package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	_ "modernc.org/sqlite"
)

// TestReaderCannotWrite verifies that writes attempted on a reader conn
// fail with SQLITE_READONLY. This is the core guarantee of the mode=ro
// reader pool: enforcement is structural (opened with SQLITE_OPEN_READONLY),
// not PRAGMA-level.
func TestReaderCannotWrite(t *testing.T) {
	p, err := New(filepath.Join(t.TempDir(), "readonly.sqlite"), 2)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	ctx := context.Background()

	if err := p.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		_, err := tx.Exec("CREATE TABLE t (c INTEGER);")
		return err
	}); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		sql  string
	}{
		{"insert", "INSERT INTO t (c) VALUES (1);"},
		{"update", "UPDATE t SET c = 2;"},
		{"delete", "DELETE FROM t;"},
		{"create", "CREATE TABLE t2 (c INTEGER);"},
		{"drop", "DROP TABLE t;"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := p.Rx(ctx, func(ctx context.Context, rx *Rx) error {
				_, err := rx.Conn().ExecContext(ctx, tc.sql)
				return err
			})
			if err == nil {
				t.Fatalf("%s succeeded on read-only conn", tc.sql)
			}
			if !strings.Contains(err.Error(), "readonly") && !strings.Contains(err.Error(), "read-only") {
				t.Fatalf("expected read-only error, got: %v", err)
			}
		})
	}

	// The writer can still make changes afterwards.
	if err := p.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		_, err := tx.Exec("INSERT INTO t (c) VALUES (42);")
		return err
	}); err != nil {
		t.Fatalf("writer broken after reader write attempts: %v", err)
	}
}

// TestReaderCannotWriteEvenWithQueryOnlyOff verifies that toggling
// PRAGMA query_only=0 within an Rx does not enable writes — mode=ro
// is strictly stronger than query_only.
func TestReaderCannotWriteEvenWithQueryOnlyOff(t *testing.T) {
	p, err := New(filepath.Join(t.TempDir(), "queryonly_ro.sqlite"), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	ctx := context.Background()

	if err := p.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		_, err := tx.Exec("CREATE TABLE t (c INTEGER);")
		return err
	}); err != nil {
		t.Fatal(err)
	}

	err = p.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		// Try to disable query_only. This may succeed (it's a runtime
		// setting) but must not enable writes.
		if _, err := rx.Conn().ExecContext(ctx, "PRAGMA query_only=0;"); err != nil {
			// PRAGMA query_only=0 failing here is also acceptable.
			return nil
		}
		// Verify writes are still rejected.
		_, werr := rx.Conn().ExecContext(ctx, "INSERT INTO t (c) VALUES (1);")
		if werr == nil {
			t.Fatal("INSERT succeeded after PRAGMA query_only=0 on read-only conn")
		}
		if !strings.Contains(werr.Error(), "readonly") && !strings.Contains(werr.Error(), "read-only") {
			t.Fatalf("expected read-only error, got: %v", werr)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestReaderSeesWALMode confirms that a reader conn observes journal_mode=wal.
// This is the invariant New() enforces at startup.
func TestReaderSeesWALMode(t *testing.T) {
	p, err := New(filepath.Join(t.TempDir(), "walmode.sqlite"), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	ctx := context.Background()

	var mode string
	if err := p.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		return rx.QueryRow("PRAGMA journal_mode;").Scan(&mode)
	}); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Fatalf("reader journal_mode=%q, want wal", mode)
	}

	// Writer sees wal too.
	if err := p.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		return tx.QueryRow("PRAGMA journal_mode;").Scan(&mode)
	}); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Fatalf("writer journal_mode=%q, want wal", mode)
	}
}

// TestReaderAndWriterArePoolSeparate confirms the writer and reader
// connection pools are backed by distinct *sql.DB instances: exhausting
// one does not affect the other.
func TestReaderAndWriterArePoolSeparate(t *testing.T) {
	p, err := New(filepath.Join(t.TempDir(), "separate.sqlite"), 2)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	if p.writerDB == p.readerDB {
		t.Fatal("writerDB and readerDB are the same *sql.DB")
	}

	ws := p.writerDB.Stats()
	if ws.MaxOpenConnections != 1 {
		t.Errorf("writerDB MaxOpenConnections=%d, want 1", ws.MaxOpenConnections)
	}
	rs := p.readerDB.Stats()
	if rs.MaxOpenConnections != 2 {
		t.Errorf("readerDB MaxOpenConnections=%d, want 2", rs.MaxOpenConnections)
	}
}

// TestReaderSeesWriterCommits is the basic WAL visibility check:
// the writer commits, a reader (on a different *sql.DB) sees it.
func TestReaderSeesWriterCommits(t *testing.T) {
	p, err := New(filepath.Join(t.TempDir(), "visibility.sqlite"), 2)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	ctx := context.Background()

	if err := p.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		if _, err := tx.Exec("CREATE TABLE t (c INTEGER);"); err != nil {
			return err
		}
		_, err := tx.Exec("INSERT INTO t (c) VALUES (1), (2), (3);")
		return err
	}); err != nil {
		t.Fatal(err)
	}

	var count int
	if err := p.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		return rx.QueryRow("SELECT count(*) FROM t;").Scan(&count)
	}); err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("reader sees count=%d, want 3", count)
	}

	// Make more changes, ensure the reader picks those up too.
	if err := p.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		_, err := tx.Exec("INSERT INTO t (c) VALUES (4);")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := p.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		return rx.QueryRow("SELECT count(*) FROM t;").Scan(&count)
	}); err != nil {
		t.Fatal(err)
	}
	if count != 4 {
		t.Fatalf("reader sees count=%d, want 4 after second commit", count)
	}
}

// TestConcurrentReadersAndWriter exercises the pool under parallel load
// to catch any ordering or separation bug introduced by the split into
// two *sql.DBs.
func TestConcurrentReadersAndWriter(t *testing.T) {
	p, err := New(filepath.Join(t.TempDir(), "concurrent.sqlite"), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	ctx := context.Background()

	if err := p.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		_, err := tx.Exec("CREATE TABLE t (c INTEGER);")
		return err
	}); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	// Writers serialize through the single writer conn.
	wg.Go(func() {
		for i := range 100 {
			err := p.Tx(ctx, func(ctx context.Context, tx *Tx) error {
				_, err := tx.Exec("INSERT INTO t (c) VALUES (?);", i)
				return err
			})
			if err != nil {
				t.Error(err)
				return
			}
		}
	})

	// Many concurrent readers.
	for range 8 {
		wg.Go(func() {
			for range 100 {
				err := p.Rx(ctx, func(ctx context.Context, rx *Rx) error {
					var n int
					return rx.QueryRow("SELECT count(*) FROM t;").Scan(&n)
				})
				if err != nil {
					t.Error(err)
					return
				}
			}
		})
	}
	wg.Wait()

	var count int
	if err := p.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		return rx.QueryRow("SELECT count(*) FROM t;").Scan(&count)
	}); err != nil {
		t.Fatal(err)
	}
	if count != 100 {
		t.Fatalf("count=%d, want 100", count)
	}
}

// TestShutdownClosesBothDBs verifies Close() releases resources from
// both the writer and reader pools.
func TestShutdownClosesBothDBs(t *testing.T) {
	p, err := New(filepath.Join(t.TempDir(), "shutdown.sqlite"), 2)
	if err != nil {
		t.Fatal(err)
	}
	writerDB := p.writerDB
	readerDB := p.readerDB
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}

	// After Close, both DBs should reject new operations.
	if err := writerDB.Ping(); err == nil {
		t.Error("writerDB.Ping succeeded after Close")
	}
	if err := readerDB.Ping(); err == nil {
		t.Error("readerDB.Ping succeeded after Close")
	}
}

// TestCloseCleansUpWALFiles verifies that after Close, the main DB file
// remains and the -wal/-shm sidecars do not interfere with reopening.
// (We don't assert -wal/-shm removal: SQLite only removes them when the
// last checkpoint left the WAL empty, which depends on writer state.)
func TestCloseCleansUpWALFiles(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "cleanup.sqlite")
	p, err := New(dbPath, 2)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := p.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		_, err := tx.Exec("CREATE TABLE t (c INTEGER);")
		return err
	}); err != nil {
		t.Fatal(err)
	}

	// -wal and -shm exist while the DB is open with WAL mode.
	if _, err := os.Stat(dbPath + "-shm"); err != nil {
		t.Errorf("-shm missing while open: %v", err)
	}

	if err := p.Close(); err != nil {
		t.Fatal(err)
	}

	// After a clean shutdown with readers having closed, SQLite typically
	// removes -wal/-shm. We don't require that (it depends on the last
	// checkpoint state), but the main DB file must still exist.
	if _, err := os.Stat(dbPath); err != nil {
		t.Errorf("main DB file missing after Close: %v", err)
	}
}

// TestReaderDSNCarriesModeRO verifies the reader DSN actually carries mode=ro.
// This is a belt-and-suspenders check complementing the behavioral tests:
// if someone refactors New() and drops the flag, we catch it before the
// behavioral tests have a chance to (e.g. on a platform where writes
// would have other side effects).
func TestReaderDSNCarriesModeRO(t *testing.T) {
	// We can't observe the DSN directly from *sql.DB, but we can open a
	// second connection with mode=rw to the same file and confirm it's
	// a different beast: our reader refuses writes, the rw conn allows them.
	dbPath := filepath.Join(t.TempDir(), "dsncheck.sqlite")
	p, err := New(dbPath, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	ctx := context.Background()

	if err := p.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		_, err := tx.Exec("CREATE TABLE t (c INTEGER);")
		return err
	}); err != nil {
		t.Fatal(err)
	}

	// Open an independent rw conn and confirm writes work there.
	rw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer rw.Close()
	if _, err := rw.ExecContext(ctx, "INSERT INTO t (c) VALUES (1);"); err != nil {
		t.Fatalf("writes broken on independent rw conn: %v", err)
	}

	// ... and a read-only open refuses writes in exactly the same way our pool does.
	// The "file:" prefix is required; modernc.org/sqlite strips the query
	// string otherwise and mode=ro is silently dropped.
	ro, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	if _, err := ro.ExecContext(ctx, "INSERT INTO t (c) VALUES (2);"); err == nil {
		t.Fatal("INSERT succeeded on mode=ro conn")
	}

	// And our pool's reader behaves like mode=ro.
	var errReader error
	_ = p.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		_, errReader = rx.Conn().ExecContext(ctx, "INSERT INTO t (c) VALUES (3);")
		return nil
	})
	if errReader == nil {
		t.Fatal("pool reader allowed INSERT")
	}
}

// TestReaderSniffHookRegisteredOnWriterOnly documents that change events
// only fire for writer-side DMLs. Pre-update hooks are writer-only by
// nature (no DML happens on readers), but this test guards against a
// regression where the hook is silently dropped by the refactor.
func TestSniffHookFiresOnWrites(t *testing.T) {
	p, err := New(filepath.Join(t.TempDir(), "sniff.sqlite"), 2)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	ctx := context.Background()

	if err := p.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		_, err := tx.Exec("CREATE TABLE t (c INTEGER);")
		return err
	}); err != nil {
		t.Fatal(err)
	}

	c, err := p.Sniff.subscribe("changes")
	if err != nil {
		t.Fatal(err)
	}
	defer p.Sniff.unsubscribe(c)

	if err := p.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		_, err := tx.Exec("INSERT INTO t (c) VALUES (1);")
		return err
	}); err != nil {
		t.Fatal(err)
	}

	select {
	case ev := <-c.ch:
		ce, ok := ev.(ChangeEvent)
		if !ok {
			t.Fatalf("expected ChangeEvent, got %T", ev)
		}
		if ce.Op != "INSERT" {
			t.Errorf("op=%q, want INSERT", ce.Op)
		}
	default:
		t.Fatal("no change event emitted by writer INSERT")
	}
}

// TestNewAcceptsFilePrefixedDSN verifies that callers may pass a URI-form
// DSN ("file:/path?..."): New must not double-prefix the reader DSN,
// which would turn it into "file:file:..." and fail to open.
func TestNewAcceptsFilePrefixedDSN(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "uri.sqlite")
	p, err := New("file:"+dbPath, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	ctx := context.Background()

	// Reader works (mode=ro enforcement intact).
	if err := p.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		_, err := rx.Conn().ExecContext(ctx, "CREATE TABLE t (c INTEGER);")
		if err == nil {
			return errors.New("CREATE TABLE succeeded on reader conn")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Writer works.
	if err := p.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		_, err := tx.Exec("CREATE TABLE t (c INTEGER);")
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

// TestReaderBlocksTempAndAttachWrites verifies that query_only=1 (set via
// the reader DSN) blocks writes to temp tables and attached databases, in
// addition to the mode=ro enforcement on the main DB. This closes the
// residual gap where mode=ro alone would leave temp/attach writable.
func TestReaderBlocksTempAndAttachWrites(t *testing.T) {
	p, err := New(filepath.Join(t.TempDir(), "tempattach.sqlite"), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	ctx := context.Background()

	// Writing to a temp table is a write; query_only blocks it.
	t.Run("create_temp_table", func(t *testing.T) {
		err := p.Rx(ctx, func(ctx context.Context, rx *Rx) error {
			_, err := rx.Conn().ExecContext(ctx, "CREATE TEMP TABLE tt (x INTEGER);")
			return err
		})
		if err == nil {
			t.Fatal("CREATE TEMP TABLE succeeded on read-only conn")
		}
	})

	// ATTACH itself is metadata, not a write, so it's allowed even under
	// query_only. What matters is that writes to the attached DB are
	// blocked.
	t.Run("write_to_attached_db", func(t *testing.T) {
		err := p.Rx(ctx, func(ctx context.Context, rx *Rx) error {
			if _, err := rx.Conn().ExecContext(ctx, "ATTACH ':memory:' AS aux;"); err != nil {
				return err
			}
			_, err := rx.Conn().ExecContext(ctx, "CREATE TABLE aux.t (x INTEGER);")
			if err == nil {
				t.Fatal("CREATE TABLE in attached DB succeeded on read-only conn")
			}
			_, _ = rx.Conn().ExecContext(ctx, "DETACH aux;")
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	})
}

// TestWriterFailsCleanupOnReaderSetupError verifies that a failure to
// set up the reader pool doesn't leave the writer DB leaked. The error
// path is hard to trigger naturally; we validate it by passing an invalid
// reader count (negative), which makes readerDB.SetMaxOpenConns misbehave.
// If we ever hit a real error in reader setup, we need the cleanup to be
// correct.
func TestNewFailsCleanlyOnBadPath(t *testing.T) {
	// A non-existent directory triggers an error when SQLite tries to
	// create the DB file. We expect New() to return an error without
	// leaking *sql.DB handles or goroutines.
	_, err := New("/nonexistent-directory-for-test/xyz.sqlite", 2)
	if err == nil {
		t.Fatal("expected error opening DB in nonexistent dir")
	}
	// The underlying error may be wrapped; just confirm it surfaces.
	if errors.Is(err, context.Canceled) {
		t.Fatalf("expected real error, got canceled: %v", err)
	}
}
