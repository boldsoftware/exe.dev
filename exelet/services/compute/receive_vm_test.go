package compute

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"
)

// busyDatasetStorage simulates a storage manager whose Delete blocks until
// all in-flight zfs recv processes have exited — matching real zfs destroy
// behavior on a busy dataset. The test passes a readerDone channel that
// closes when the simulated zfs recv exits; Delete blocks on it.
type busyDatasetStorage struct {
	readerDone    <-chan struct{} // Delete blocks until this is closed (simulates busy dataset)
	deleteEntered chan struct{}   // closed when Delete is entered
}

func (m *busyDatasetStorage) Delete(_ context.Context, _ string) error {
	close(m.deleteEntered)
	// Simulate zfs destroy blocking on a busy dataset: wait for the
	// reader (zfs recv) to exit before returning.
	<-m.readerDone
	return nil
}

type mockDeleteNetworkManager struct{}

func (m *mockDeleteNetworkManager) DeleteInterface(_ context.Context, _, _ string) error {
	return nil
}

type testLogger struct{}

func (l *testLogger) WarnContext(_ context.Context, _ string, _ ...any) {}

// TestRollbackClosesRecvWritersBeforeDelete verifies that Rollback terminates
// in-flight zfs recv pipe writers before calling storageManager.Delete.
//
// It simulates the production failure mode:
//   - A zfs recv process holds a dataset open via a pipe reader
//   - zfs destroy (Delete) blocks while the dataset is busy
//   - Rollback must close the pipe writer first so zfs recv exits,
//     which unblocks zfs destroy
//
// If closeRecvWriters were removed, Delete would block forever and the test
// would time out.
func TestRollbackClosesRecvWritersBeforeDelete(t *testing.T) {
	t.Parallel()

	// Simulate an in-flight zfs recv: a reader goroutine that blocks
	// on the pipe until it's closed.
	pr, pw := io.Pipe()
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		io.Copy(io.Discard, pr)
	}()

	storage := &busyDatasetStorage{
		readerDone:    readerDone,
		deleteEntered: make(chan struct{}),
	}
	rb := &receiveVMRollback{
		ctx:               context.Background(),
		log:               &testLogger{},
		storageManager:    storage,
		networkManager:    &mockDeleteNetworkManager{},
		instanceID:        "test-instance",
		instanceDir:       t.TempDir(),
		zfsDatasetCreated: true,
	}
	rb.trackRecvWriter(pw)

	// Rollback must: close pipe writer → reader exits → Delete unblocks → returns.
	// If the pipe writer is NOT closed before Delete, Delete blocks on
	// readerDone forever and this times out.
	rollbackDone := make(chan struct{})
	go func() {
		defer close(rollbackDone)
		rb.Rollback()
	}()

	select {
	case <-rollbackDone:
		// Rollback completed — pipe was closed before Delete, proving ordering.
	case <-time.After(5 * time.Second):
		t.Fatal("Rollback hung: pipe writer was not closed before Delete")
	}
}

func TestRollbackWithNoActiveWriters(t *testing.T) {
	t.Parallel()

	deleteEntered := make(chan struct{})
	alreadyClosed := make(chan struct{})
	close(alreadyClosed)
	storage := &busyDatasetStorage{
		readerDone:    alreadyClosed,
		deleteEntered: deleteEntered,
	}

	rb := &receiveVMRollback{
		ctx:               context.Background(),
		log:               &testLogger{},
		storageManager:    storage,
		networkManager:    &mockDeleteNetworkManager{},
		instanceID:        "test-instance",
		instanceDir:       t.TempDir(),
		zfsDatasetCreated: true,
	}

	// No pipe writers tracked — Rollback should still call Delete and return.
	rb.Rollback()

	select {
	case <-deleteEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("storageManager.Delete was never called")
	}
}

func TestRollbackClosesMultipleWriters(t *testing.T) {
	t.Parallel()

	// Simulate two in-flight zfs recv processes (phase 1 + phase 2).
	// Delete blocks until ALL readers exit.
	allReadersDone := make(chan struct{})
	var readersWg sync.WaitGroup

	rb := &receiveVMRollback{
		ctx:               context.Background(),
		log:               &testLogger{},
		storageManager:    &busyDatasetStorage{readerDone: allReadersDone, deleteEntered: make(chan struct{})},
		networkManager:    &mockDeleteNetworkManager{},
		instanceID:        "test-instance",
		instanceDir:       t.TempDir(),
		zfsDatasetCreated: true,
	}

	for range 2 {
		pr, pw := io.Pipe()
		rb.trackRecvWriter(pw)
		readersWg.Add(1)
		go func() {
			defer readersWg.Done()
			io.Copy(io.Discard, pr)
		}()
	}

	// Close allReadersDone when both reader goroutines finish.
	go func() {
		readersWg.Wait()
		close(allReadersDone)
	}()

	rollbackDone := make(chan struct{})
	go func() {
		defer close(rollbackDone)
		rb.Rollback()
	}()

	select {
	case <-rollbackDone:
	case <-time.After(5 * time.Second):
		t.Fatal("Rollback hung: not all pipe writers were closed before Delete")
	}
}
