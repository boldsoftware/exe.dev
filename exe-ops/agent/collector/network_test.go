package collector

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestNetworkCollect(t *testing.T) {
	content := `Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
    lo: 1000000     100    0    0    0     0          0         0  1000000     100    0    0    0     0       0          0
  eth0: 5000000    5000    0    0    0     0          0         0  3000000    3000    0    0    0     0       0          0
  eth1: 2000000    2000    0    0    0     0          0         0  1000000    1000    0    0    0     0       0          0
`
	dir := t.TempDir()
	path := filepath.Join(dir, "dev")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	n := &Network{procPath: path}

	// First collection — no previous baseline, should report zero.
	if err := n.Collect(context.Background()); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if n.Recv != 0 {
		t.Errorf("first Recv = %d, want 0", n.Recv)
	}
	if n.Send != 0 {
		t.Errorf("first Send = %d, want 0", n.Send)
	}

	// Second collection with same data — delta should be zero.
	if err := n.Collect(context.Background()); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if n.Recv != 0 {
		t.Errorf("same-data Recv = %d, want 0", n.Recv)
	}
	if n.Send != 0 {
		t.Errorf("same-data Send = %d, want 0", n.Send)
	}

	// Write updated counters — eth0 gained 1000 recv, 500 send.
	content2 := `Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
    lo: 1000000     100    0    0    0     0          0         0  1000000     100    0    0    0     0       0          0
  eth0: 5001000    5001    0    0    0     0          0         0  3000500    3001    0    0    0     0       0          0
  eth1: 2000000    2000    0    0    0     0          0         0  1000000    1000    0    0    0     0       0          0
`
	if err := os.WriteFile(path, []byte(content2), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := n.Collect(context.Background()); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if n.Recv != 1000 {
		t.Errorf("delta Recv = %d, want 1000", n.Recv)
	}
	if n.Send != 500 {
		t.Errorf("delta Send = %d, want 500", n.Send)
	}
}

func TestNetworkErrorsAndDrops(t *testing.T) {
	content := `Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
    lo: 1000000     100    0    0    0     0          0         0  1000000     100    0    0    0     0       0          0
  eth0: 5000000    5000    3    1    0     0          0         0  3000000    3000    2    4    0     0       0          0
  eth1: 2000000    2000    1    0    0     0          0         0  1000000    1000    0    1    0     0       0          0
`
	dir := t.TempDir()
	path := filepath.Join(dir, "dev")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	n := &Network{procPath: path}
	if err := n.Collect(context.Background()); err != nil {
		t.Fatalf("Collect: %v", err)
	}

	// Cumulative totals across eth0 + eth1 (lo excluded).
	if n.RxErrors != 4 { // 3+1
		t.Errorf("RxErrors = %d, want 4", n.RxErrors)
	}
	if n.RxDropped != 1 { // 1+0
		t.Errorf("RxDropped = %d, want 1", n.RxDropped)
	}
	if n.TxErrors != 2 { // 2+0
		t.Errorf("TxErrors = %d, want 2", n.TxErrors)
	}
	if n.TxDropped != 5 { // 4+1
		t.Errorf("TxDropped = %d, want 5", n.TxDropped)
	}
}
