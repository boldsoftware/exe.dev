package execore

import (
	"fmt"
	"sync"
	"testing"
	"unsafe"
)

func TestLocalMigrateOp_RecordAndSnapshot(t *testing.T) {
	op := &localMigrateOp{Hostname: "host-a", Total: 2}

	op.record(localMigrateVMRes{ID: "vm-1", OK: true, DowntimeMs: 100})
	op.record(localMigrateVMRes{ID: "vm-2", OK: false, Error: "boom"})

	snap := op.snapshot()
	if snap.Hostname != "host-a" {
		t.Fatalf("hostname = %q, want host-a", snap.Hostname)
	}
	if snap.Done != 2 {
		t.Fatalf("done = %d, want 2", snap.Done)
	}
	if len(snap.Results) != 2 {
		t.Fatalf("results len = %d, want 2", len(snap.Results))
	}
	if snap.Results[0].ID != "vm-1" || snap.Results[1].ID != "vm-2" {
		t.Fatalf("unexpected result ordering: %+v", snap.Results)
	}
}

// TestLocalMigrateOp_SnapshotIsolation verifies that mutations to op.Results
// after snapshot() returns do not appear in the snapshot's copy.
func TestLocalMigrateOp_SnapshotIsolation(t *testing.T) {
	op := &localMigrateOp{}
	op.record(localMigrateVMRes{ID: "vm-1"})

	snap := op.snapshot()

	// Record another entry after taking the snapshot.
	op.record(localMigrateVMRes{ID: "vm-2"})

	if len(snap.Results) != 1 {
		t.Fatalf("snapshot len = %d, want 1 (should not reflect later record)", len(snap.Results))
	}

	// Also check the backing arrays are distinct — appending to op should
	// not mutate snap's slice, regardless of cap growth.
	snapData := unsafe.SliceData(snap.Results)
	opData := unsafe.SliceData(op.Results)
	if snapData == opData {
		t.Fatal("snapshot shares backing array with op.Results")
	}
}

// TestLocalMigrateOp_ConcurrentRecordSnapshot hammers record and snapshot
// concurrently. Run with -race to catch unsynchronized access.
func TestLocalMigrateOp_ConcurrentRecordSnapshot(t *testing.T) {
	op := &localMigrateOp{Hostname: "host-a"}

	const writers = 16
	const perWriter = 200

	writersDone := make(chan struct{})
	var wg sync.WaitGroup
	for w := range writers {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := range perWriter {
				op.record(localMigrateVMRes{ID: fmt.Sprintf("w%d-%d", w, i), OK: true})
			}
		}(w)
	}
	go func() {
		wg.Wait()
		close(writersDone)
	}()

	// Reader takes snapshots until writers finish. We don't assert on
	// content — just that there are no races and no panics.
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		for {
			select {
			case <-writersDone:
				return
			default:
				_ = op.snapshot()
			}
		}
	}()

	<-writersDone
	<-readerDone

	snap := op.snapshot()
	if snap.Done != writers*perWriter {
		t.Fatalf("done = %d, want %d", snap.Done, writers*perWriter)
	}
	if len(snap.Results) != writers*perWriter {
		t.Fatalf("results len = %d, want %d", len(snap.Results), writers*perWriter)
	}
}
