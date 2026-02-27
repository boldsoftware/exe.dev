package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type mockExecutor struct {
	mu           sync.Mutex
	startCount   atomic.Int32
	destroyCount atomic.Int32
	startDelay   time.Duration
	startErr     error
}

func (m *mockExecutor) StartVM(ctx context.Context, name, opsDir string) (*VMInfo, error) {
	m.startCount.Add(1)
	if m.startDelay > 0 {
		select {
		case <-time.After(m.startDelay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	m.mu.Lock()
	err := m.startErr
	m.mu.Unlock()
	if err != nil {
		return nil, err
	}

	// Write a temporary env file so DestroyVM has something to read.
	dir, _ := os.MkdirTemp("", "mock-vm-")
	envFile := filepath.Join(dir, name+".env")
	os.WriteFile(envFile, []byte(fmt.Sprintf("VM_NAME=%s\nVM_IP=192.168.122.%d\nVM_USER=ubuntu\n", name, m.startCount.Load())), 0o644)

	return &VMInfo{
		Name:    name,
		IP:      fmt.Sprintf("192.168.122.%d", m.startCount.Load()),
		User:    "ubuntu",
		EnvFile: envFile,
	}, nil
}

func (m *mockExecutor) DestroyVM(ctx context.Context, envFile, opsDir string) error {
	m.destroyCount.Add(1)
	os.Remove(envFile)
	os.Remove(filepath.Dir(envFile))
	return nil
}

func (m *mockExecutor) DestroyVMByName(ctx context.Context, name, opsDir string) {
	m.destroyCount.Add(1)
}

func TestPoolCreatesVMs(t *testing.T) {
	mock := &mockExecutor{startDelay: 10 * time.Millisecond}
	pool := NewPool(3, time.Hour, "/tmp/ops", mock)
	pool.Start()
	defer pool.Stop()

	// Wait for all slots to become ready.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		status := pool.Status()
		if status.Ready == 3 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	status := pool.Status()
	if status.Ready != 3 {
		t.Fatalf("expected 3 ready, got %+v", status)
	}
	if int(mock.startCount.Load()) < 3 {
		t.Fatalf("expected at least 3 starts, got %d", mock.startCount.Load())
	}
}

func TestPoolClaimAndRelease(t *testing.T) {
	mock := &mockExecutor{startDelay: 10 * time.Millisecond}
	pool := NewPool(2, time.Hour, "/tmp/ops", mock)
	pool.Start()
	defer pool.Stop()

	// Wait for ready.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if pool.Status().Ready >= 1 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	ctx := context.Background()
	vm, idx, err := pool.Claim(ctx, "test-run-1")
	if err != nil {
		t.Fatal(err)
	}
	if vm.IP == "" {
		t.Fatal("expected non-empty IP")
	}

	status := pool.Status()
	if status.Claimed != 1 {
		t.Fatalf("expected 1 claimed, got %+v", status)
	}

	// Release triggers destruction and recreation.
	pool.Release(idx)

	// Wait for the slot to be recreated.
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		status = pool.Status()
		if status.Ready == 2 && status.Claimed == 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	status = pool.Status()
	if status.Ready != 2 {
		t.Fatalf("expected 2 ready after release, got %+v", status)
	}
}

func TestPoolClaimBlocks(t *testing.T) {
	mock := &mockExecutor{startDelay: 200 * time.Millisecond}
	pool := NewPool(1, time.Hour, "/tmp/ops", mock)
	pool.Start()
	defer pool.Stop()

	// Wait for the single slot to be ready.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if pool.Status().Ready >= 1 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Claim the only slot.
	ctx := context.Background()
	_, idx, err := pool.Claim(ctx, "run-1")
	if err != nil {
		t.Fatal(err)
	}

	// Second claim should block until we release.
	done := make(chan error, 1)
	go func() {
		ctx2, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _, err := pool.Claim(ctx2, "run-2")
		done <- err
	}()

	// Give the goroutine a moment to start waiting.
	time.Sleep(50 * time.Millisecond)

	// Release the first slot.
	pool.Release(idx)

	// Second claim should succeed after the slot is recreated.
	if err := <-done; err != nil {
		t.Fatalf("second claim failed: %v", err)
	}
}

func TestPoolClaimTimeout(t *testing.T) {
	mock := &mockExecutor{startDelay: 10 * time.Second}
	pool := NewPool(1, time.Hour, "/tmp/ops", mock)
	pool.Start()
	defer pool.Stop()

	// Don't wait for ready — claim with a short timeout.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, _, err := pool.Claim(ctx, "run-timeout")
	if err == nil {
		t.Fatal("expected timeout error")
	}
}

func TestPoolRecycleStale(t *testing.T) {
	mock := &mockExecutor{startDelay: 10 * time.Millisecond}
	// Very short max idle to trigger recycling quickly.
	pool := NewPool(1, 100*time.Millisecond, "/tmp/ops", mock)
	pool.Start()
	defer pool.Stop()

	// Wait for initial ready.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if pool.Status().Ready >= 1 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	initialStarts := mock.startCount.Load()

	// Wait for recycling to happen (the maintenance loop checks every 5s,
	// but with 100ms maxIdle it should recycle on the first tick).
	deadline = time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if mock.startCount.Load() > initialStarts {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	if mock.startCount.Load() <= initialStarts {
		t.Fatal("expected VM to be recycled")
	}
	if mock.destroyCount.Load() < 1 {
		t.Fatal("expected at least one destroy for recycling")
	}
}

func TestPoolRecycle(t *testing.T) {
	mock := &mockExecutor{startDelay: 10 * time.Millisecond}
	pool := NewPool(3, time.Hour, "/tmp/ops", mock)
	pool.Start()
	defer pool.Stop()

	// Wait for all slots to become ready.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if pool.Status().Ready == 3 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if pool.Status().Ready != 3 {
		t.Fatalf("expected 3 ready, got %+v", pool.Status())
	}

	startsBeforeRecycle := mock.startCount.Load()
	destroysBeforeRecycle := mock.destroyCount.Load()

	// Claim one slot so it's not recycled.
	ctx := context.Background()
	_, _, err := pool.Claim(ctx, "hold")
	if err != nil {
		t.Fatal(err)
	}

	// Recycle should only affect ready (unclaimed) slots.
	n := pool.Recycle()
	if n != 2 {
		t.Fatalf("expected 2 recycled, got %d", n)
	}

	// Wait for the recycled slots to come back.
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		s := pool.Status()
		if s.Ready == 2 && s.Claimed == 1 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	s := pool.Status()
	if s.Ready != 2 || s.Claimed != 1 {
		t.Fatalf("expected 2 ready + 1 claimed, got %+v", s)
	}

	// Verify new VMs were created and old ones destroyed.
	if mock.startCount.Load() < startsBeforeRecycle+2 {
		t.Fatalf("expected at least 2 new starts, got %d total", mock.startCount.Load())
	}
	if mock.destroyCount.Load() < destroysBeforeRecycle+2 {
		t.Fatalf("expected at least 2 destroys, got %d total", mock.destroyCount.Load())
	}
}

func TestPoolStatus(t *testing.T) {
	mock := &mockExecutor{startDelay: 10 * time.Millisecond}
	pool := NewPool(4, time.Hour, "/tmp/ops", mock)
	pool.Start()
	defer pool.Stop()

	// Wait for all ready.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if pool.Status().Ready == 4 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	status := pool.Status()
	if status.Target != 4 {
		t.Fatalf("target: got %d, want 4", status.Target)
	}
	if status.Ready != 4 {
		t.Fatalf("ready: got %d, want 4", status.Ready)
	}
}

func TestParseEnvFile(t *testing.T) {
	dir := t.TempDir()
	envFile := filepath.Join(dir, "test.env")
	os.WriteFile(envFile, []byte("VM_NAME=test-vm\nVM_IP=10.0.0.1\nVM_USER=ubuntu\nVM_DISK=/tmp/disk.qcow2\nVM_DATA_DISK=/tmp/data.qcow2\nVM_SEED=/tmp/seed.iso\n"), 0o644)

	vm, err := parseEnvFile(envFile)
	if err != nil {
		t.Fatal(err)
	}
	if vm.Name != "test-vm" {
		t.Fatalf("name: got %q, want %q", vm.Name, "test-vm")
	}
	if vm.IP != "10.0.0.1" {
		t.Fatalf("ip: got %q, want %q", vm.IP, "10.0.0.1")
	}
	if vm.User != "ubuntu" {
		t.Fatalf("user: got %q, want %q", vm.User, "ubuntu")
	}
}

func TestParseEnvFileMissingFields(t *testing.T) {
	dir := t.TempDir()
	envFile := filepath.Join(dir, "test.env")
	os.WriteFile(envFile, []byte("VM_NAME=test-vm\n"), 0o644)

	_, err := parseEnvFile(envFile)
	if err == nil {
		t.Fatal("expected error for missing VM_IP")
	}
}
