// Package exeworker defines the interface for workers that run on exes.
package exeworker

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"exe.dev/exedb"
	"exe.dev/sqlite"
)

// Event is a named type for events that workers can register for.
type Event string

// Job is a unit of work dispatched to a worker.
type Job struct {
	ID      int64
	Event   Event
	Payload []byte
}

// Worker is the interface for a unit of work that runs on an exe.
type Worker interface {
	// Run executes the worker for the given job. It blocks until the
	// work is complete or the context is canceled.
	Run(ctx context.Context, job Job) error
}

// Registry is a concurrency-safe collection of workers keyed by event.
type Registry struct {
	mu       sync.Mutex
	handlers map[Event][]Worker
	queue    chan Job
}

// NewRegistry creates a ready-to-use Registry.
func NewRegistry() *Registry {
	return &Registry{
		handlers: make(map[Event][]Worker),
		queue:    make(chan Job, 128),
	}
}

// Register associates a worker with one or more events.
// It returns an error if worker is nil or no events are provided.
func (r *Registry) Register(worker Worker, events ...Event) error {
	if worker == nil {
		return fmt.Errorf("exeworker: worker must not be nil")
	}
	if len(events) == 0 {
		return fmt.Errorf("exeworker: at least one event is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, event := range events {
		r.handlers[event] = append(r.handlers[event], worker)
	}
	return nil
}

// Publish persists a job to SQLite and sends it to the dispatch channel.
func (r *Registry) Publish(ctx context.Context, db *sqlite.DB, event Event, payload []byte) error {
	var job Job
	err := exedb.WithTx(db, ctx, func(ctx context.Context, q *exedb.Queries) error {
		row, err := q.InsertWorkerJob(ctx, exedb.InsertWorkerJobParams{
			Event:   string(event),
			Payload: payload,
		})
		if err != nil {
			return err
		}
		job = Job{ID: row.ID, Event: event, Payload: payload}
		return nil
	})
	if err != nil {
		return fmt.Errorf("exeworker: insert job: %w", err)
	}
	r.queue <- job
	return nil
}

// Recover replays pending jobs from SQLite onto the dispatch channel.
// Call this on startup before Run to pick up jobs that were not completed.
func (r *Registry) Recover(ctx context.Context, db *sqlite.DB) error {
	var jobs []Job
	err := exedb.WithRx(db, ctx, func(ctx context.Context, q *exedb.Queries) error {
		rows, err := q.ListPendingWorkerJobs(ctx)
		if err != nil {
			return err
		}
		for _, row := range rows {
			jobs = append(jobs, Job{ID: row.ID, Event: Event(row.Event), Payload: row.Payload})
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("exeworker: list pending jobs: %w", err)
	}
	for _, job := range jobs {
		r.queue <- job
	}
	return nil
}

// Run is the dispatch loop. It reads jobs from the channel and fans out
// to registered workers. It blocks until ctx is canceled.
func (r *Registry) Run(ctx context.Context, db *sqlite.DB) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case job := <-r.queue:
			r.dispatch(ctx, db, job)
		}
	}
}

func (r *Registry) dispatch(ctx context.Context, db *sqlite.DB, job Job) {
	r.mu.Lock()
	workers := r.handlers[job.Event]
	r.mu.Unlock()

	_ = exedb.WithTx(db, ctx, func(ctx context.Context, q *exedb.Queries) error {
		return q.UpdateWorkerJobStatus(ctx, exedb.UpdateWorkerJobStatusParams{
			Status: "running",
			ID:     job.ID,
		})
	})

	status := "done"
	for _, w := range workers {
		if err := w.Run(ctx, job); err != nil {
			slog.ErrorContext(ctx, "exeworker: worker failed", "event", job.Event, "job_id", job.ID, "err", err)
			status = "failed"
		}
	}

	if err := exedb.WithTx(db, ctx, func(ctx context.Context, q *exedb.Queries) error {
		return q.UpdateWorkerJobStatus(ctx, exedb.UpdateWorkerJobStatusParams{
			Status: status,
			ID:     job.ID,
		})
	}); err != nil {
		slog.ErrorContext(ctx, "exeworker: update job status", "job_id", job.ID, "status", status, "err", err)
	}
}
