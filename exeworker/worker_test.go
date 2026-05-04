package exeworker_test

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"

	"exe.dev/exedb"
	"exe.dev/exeworker"
	"exe.dev/sqlite"
	"exe.dev/tslog"
)

func testDB(t *testing.T) *sqlite.DB {
	t.Helper()
	dbPath := t.TempDir() + "/test.db"
	rawDB, err := sql.Open("sqlite", sqlite.WithTimeParams(dbPath))
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlite.InitDB(rawDB, 1); err != nil {
		t.Fatal(err)
	}
	if err := exedb.RunMigrations(tslog.Slogger(t), rawDB); err != nil {
		t.Fatal(err)
	}
	rawDB.Close()

	db, err := sqlite.New(dbPath, 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// stubWorker is a minimal Worker implementation for compile-time
// interface verification.
type stubWorker struct {
	run func(ctx context.Context, job exeworker.Job) error
}

func (w *stubWorker) Run(ctx context.Context, job exeworker.Job) error { return w.run(ctx, job) }

var _ exeworker.Worker = (*stubWorker)(nil)

func TestWorkerInterface(t *testing.T) {
	w := &stubWorker{
		run: func(ctx context.Context, job exeworker.Job) error { return nil },
	}
	job := exeworker.Job{ID: 1, Event: "test", Payload: []byte(`{"key":"value"}`)}
	if err := w.Run(context.Background(), job); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRegister(t *testing.T) {
	r := exeworker.NewRegistry()
	w := &stubWorker{run: func(ctx context.Context, job exeworker.Job) error { return nil }}
	if err := r.Register(w, "build", "test"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRegisterNoEvents(t *testing.T) {
	r := exeworker.NewRegistry()
	w := &stubWorker{run: func(ctx context.Context, job exeworker.Job) error { return nil }}
	if err := r.Register(w); err == nil {
		t.Fatal("expected error for zero events, got nil")
	}
}

func TestRegisterNilWorker(t *testing.T) {
	r := exeworker.NewRegistry()
	if err := r.Register(nil, "build"); err == nil {
		t.Fatal("expected error for nil worker, got nil")
	}
}

func TestPublishAndRun(t *testing.T) {
	db := testDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var mu sync.Mutex
	var received []exeworker.Job

	r := exeworker.NewRegistry()
	w := &stubWorker{run: func(ctx context.Context, job exeworker.Job) error {
		mu.Lock()
		received = append(received, job)
		mu.Unlock()
		return nil
	}}
	if err := r.Register(w, "deploy"); err != nil {
		t.Fatal(err)
	}

	// Start the dispatch loop in the background.
	go r.Run(ctx, db)

	if err := r.Publish(ctx, db, "deploy", []byte(`{"app":"web"}`)); err != nil {
		t.Fatal(err)
	}

	// Wait for the worker to process the job.
	for i := 0; i < 100; i++ {
		mu.Lock()
		n := len(received)
		mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(received) != 1 {
		t.Fatalf("expected 1 job, got %d", len(received))
	}
	if received[0].Event != "deploy" {
		t.Fatalf("expected event %q, got %q", "deploy", received[0].Event)
	}
	if string(received[0].Payload) != `{"app":"web"}` {
		t.Fatalf("expected payload %q, got %q", `{"app":"web"}`, string(received[0].Payload))
	}

	// Verify job status is "done" in the database.
	var status string
	exedb.WithRx(db, ctx, func(ctx context.Context, q *exedb.Queries) error {
		jobs, err := q.ListPendingWorkerJobs(ctx)
		if err != nil {
			return err
		}
		if len(jobs) != 0 {
			status = "pending"
		} else {
			status = "done"
		}
		return nil
	})
	if status != "done" {
		t.Fatalf("expected no pending jobs, got status %q", status)
	}
}

func TestRecover(t *testing.T) {
	db := testDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Insert a job directly via SQL to simulate a crash recovery scenario.
	exedb.WithTx(db, ctx, func(ctx context.Context, q *exedb.Queries) error {
		_, err := q.InsertWorkerJob(ctx, exedb.InsertWorkerJobParams{
			Event:   "build",
			Payload: []byte(`{"ref":"main"}`),
		})
		return err
	})

	var mu sync.Mutex
	var received []exeworker.Job

	r := exeworker.NewRegistry()
	w := &stubWorker{run: func(ctx context.Context, job exeworker.Job) error {
		mu.Lock()
		received = append(received, job)
		mu.Unlock()
		return nil
	}}
	if err := r.Register(w, "build"); err != nil {
		t.Fatal(err)
	}

	go r.Run(ctx, db)

	if err := r.Recover(ctx, db); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 100; i++ {
		mu.Lock()
		n := len(received)
		mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(received) != 1 {
		t.Fatalf("expected 1 recovered job, got %d", len(received))
	}
	if received[0].Event != "build" {
		t.Fatalf("expected event %q, got %q", "build", received[0].Event)
	}
}
