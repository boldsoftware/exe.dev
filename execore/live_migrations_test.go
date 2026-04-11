package execore

import (
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
	tracker.start("box-a", "tcp://src:9080", "tcp://dst:9080", true)

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
	tracker.start("box-b", "tcp://src:9080", "tcp://other:9080", false)
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
