package execore

import (
	"context"
	"testing"
	"time"
)

func TestLiveMigrationTracker(t *testing.T) {
	tracker := newLiveMigrationTracker()

	// Empty initially
	if got := tracker.snapshot(); len(got) != 0 {
		t.Fatalf("expected empty snapshot, got %d entries", len(got))
	}

	// Start a migration
	tracker.start(context.Background(), "box-a", "tcp://src:9080", "tcp://dst:9080", true)

	snap := tracker.snapshot()
	if len(snap) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(snap))
	}
	if snap[0].BoxName != "box-a" {
		t.Errorf("expected box-a, got %s", snap[0].BoxName)
	}
	if snap[0].State != liveMigrationTransferring {
		t.Errorf("expected transferring, got %s", snap[0].State)
	}
	if !snap[0].Live {
		t.Error("expected live=true")
	}

	// Update bytes
	tracker.updateBytes("box-a", 1024*1024*500)
	snap = tracker.snapshot()
	if snap[0].BytesSent != 1024*1024*500 {
		t.Errorf("expected 500MiB, got %d", snap[0].BytesSent)
	}

	// Update state
	tracker.updateState("box-a", liveMigrationReconfiguring)
	snap = tracker.snapshot()
	if snap[0].State != liveMigrationReconfiguring {
		t.Errorf("expected reconfiguring, got %s", snap[0].State)
	}

	// Start a second migration
	tracker.start(context.Background(), "box-b", "tcp://src:9080", "tcp://other:9080", false)
	if got := tracker.snapshot(); len(got) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(got))
	}

	// Filter by exelet
	forSrc := tracker.snapshotForExelet("tcp://src:9080")
	if len(forSrc) != 2 {
		t.Errorf("expected 2 for source, got %d", len(forSrc))
	}
	forDst := tracker.snapshotForExelet("tcp://dst:9080")
	if len(forDst) != 1 {
		t.Errorf("expected 1 for dst, got %d", len(forDst))
	}
	forOther := tracker.snapshotForExelet("tcp://other:9080")
	if len(forOther) != 1 {
		t.Errorf("expected 1 for other, got %d", len(forOther))
	}
	forNone := tracker.snapshotForExelet("tcp://unrelated:9080")
	if len(forNone) != 0 {
		t.Errorf("expected 0 for unrelated, got %d", len(forNone))
	}

	// Finish first migration
	tracker.finish("box-a")
	if got := tracker.snapshot(); len(got) != 1 {
		t.Fatalf("expected 1 entry after finish, got %d", len(got))
	}

	// Finish second
	tracker.finish("box-b")
	if got := tracker.snapshot(); len(got) != 0 {
		t.Fatalf("expected 0 entries after all finished, got %d", len(got))
	}

	// Update/finish on nonexistent box is a no-op
	tracker.updateBytes("nonexistent", 100)
	tracker.updateState("nonexistent", liveMigrationFinalizing)
	tracker.finish("nonexistent")
}

func TestFormatMigrationBytes(t *testing.T) {
	tests := []struct {
		input int64
		want  string
	}{
		{0, "\u2014"},
		{512 * 1024, "512 KiB"},
		{50 * 1024 * 1024, "50 MiB"},
		{3 * 1024 * 1024 * 1024, "3.0 GiB"},
	}
	for _, tt := range tests {
		got := formatMigrationBytes(tt.input)
		if got != tt.want {
			t.Errorf("formatMigrationBytes(%d) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{5 * time.Second, "5s"},
		{59 * time.Second, "59s"},
		{60 * time.Second, "1m00s"},
		{90 * time.Second, "1m30s"},
		{3661 * time.Second, "61m01s"},
	}
	for _, tt := range tests {
		got := formatDuration(tt.d)
		if got != tt.want {
			t.Errorf("formatDuration(%v) = %q, want %q", tt.d, got, tt.want)
		}
	}
}

func TestLiveMigrationTrackerCancel(t *testing.T) {
	tracker := newLiveMigrationTracker()

	// Cancel on nonexistent box returns false.
	if tracker.cancel("nonexistent") {
		t.Error("cancel on nonexistent should return false")
	}
	if tracker.cancelled("nonexistent") {
		t.Error("cancelled on nonexistent should return false")
	}

	// Start a migration and verify the returned context is not cancelled.
	ctx := tracker.start(context.Background(), "box-cancel", "tcp://src:9080", "tcp://dst:9080", true)
	if ctx.Err() != nil {
		t.Fatal("expected context to be active")
	}
	if tracker.cancelled("box-cancel") {
		t.Error("expected not cancelled initially")
	}

	// Cancel the migration.
	if !tracker.cancel("box-cancel") {
		t.Error("cancel should return true for active migration")
	}
	if ctx.Err() == nil {
		t.Error("expected context to be cancelled after cancel()")
	}
	if !tracker.cancelled("box-cancel") {
		t.Error("expected cancelled to be true after cancel()")
	}

	// The entry is still in the tracker (until finish is called).
	snap := tracker.snapshot()
	if len(snap) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(snap))
	}

	// Finish removes it.
	tracker.finish("box-cancel")
	if len(tracker.snapshot()) != 0 {
		t.Fatal("expected 0 entries after finish")
	}
}

func TestLiveMigrationTrackerBatchCancel(t *testing.T) {
	tracker := newLiveMigrationTracker()

	// Start a batch.
	ctx := tracker.startBatch(context.Background(), "user-123")
	if ctx.Err() != nil {
		t.Fatal("expected batch context to be active")
	}

	// Start a migration under this batch context.
	migCtx := tracker.start(ctx, "batch-box", "tcp://src:9080", "tcp://dst:9080", true)
	if migCtx.Err() != nil {
		t.Fatal("expected migration context to be active")
	}

	// Cancel the batch.
	if !tracker.cancelBatch("user-123") {
		t.Error("cancelBatch should return true")
	}
	if ctx.Err() == nil {
		t.Error("expected batch context to be cancelled")
	}
	// The child migration context should also be cancelled (parent cancelled).
	if migCtx.Err() == nil {
		t.Error("expected migration context to be cancelled when batch is cancelled")
	}

	// Cancel on nonexistent batch returns false.
	if tracker.cancelBatch("nonexistent") {
		t.Error("cancelBatch on nonexistent should return false")
	}

	// Cleanup.
	tracker.finish("batch-box")
	tracker.finishBatch("user-123")
}

func TestLiveMigrationTrackerCancelAll(t *testing.T) {
	tracker := newLiveMigrationTracker()

	// Cancel all on empty tracker returns 0, 0.
	migs, batches := tracker.cancelAll()
	if migs != 0 || batches != 0 {
		t.Errorf("cancelAll on empty: migs=%d, batches=%d, want 0, 0", migs, batches)
	}

	// Start two migrations and a batch.
	ctx1 := tracker.start(context.Background(), "box-1", "tcp://s:1", "tcp://d:1", true)
	ctx2 := tracker.start(context.Background(), "box-2", "tcp://s:2", "tcp://d:2", false)
	batchCtx := tracker.startBatch(context.Background(), "user-all")

	if ctx1.Err() != nil || ctx2.Err() != nil || batchCtx.Err() != nil {
		t.Fatal("all contexts should be active")
	}

	migs, batches = tracker.cancelAll()
	if migs != 2 {
		t.Errorf("cancelAll migs = %d, want 2", migs)
	}
	if batches != 1 {
		t.Errorf("cancelAll batches = %d, want 1", batches)
	}

	if ctx1.Err() == nil {
		t.Error("expected ctx1 cancelled")
	}
	if ctx2.Err() == nil {
		t.Error("expected ctx2 cancelled")
	}
	if batchCtx.Err() == nil {
		t.Error("expected batchCtx cancelled")
	}

	// Both should be marked cancelled.
	if !tracker.cancelled("box-1") || !tracker.cancelled("box-2") {
		t.Error("expected both migrations marked cancelled")
	}

	// Calling cancelAll again is idempotent (already cancelled).
	migs, batches = tracker.cancelAll()
	if migs != 0 {
		t.Errorf("second cancelAll migs = %d, want 0 (already cancelled)", migs)
	}
	if batches != 1 {
		// batch cancel funcs are still in the map (not removed until finishBatch)
		// but calling cancel again is harmless.
		t.Errorf("second cancelAll batches = %d, want 1", batches)
	}

	tracker.finish("box-1")
	tracker.finish("box-2")
	tracker.finishBatch("user-all")
}
