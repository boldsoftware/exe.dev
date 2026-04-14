//go:build linux

package cloudhypervisor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"exe.dev/exelet/vmm/cloudhypervisor/client"
	api "exe.dev/pkg/api/exe/compute/v1"
)

func TestIsStopped(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"ErrNotConnected", client.ErrNotConnected, true},
		{"wrapped ErrNotConnected", fmt.Errorf("dial: %w", client.ErrNotConnected), true},
		{"EOF", io.EOF, true},
		{"ErrUnexpectedEOF", io.ErrUnexpectedEOF, true},
		{"ErrClosedPipe", io.ErrClosedPipe, true},
		{"ErrNotExist", fs.ErrNotExist, true},
		{"DeadlineExceeded", context.DeadlineExceeded, true},
		{"wrapped DeadlineExceeded", fmt.Errorf("request: %w", context.DeadlineExceeded), true},
		{"Client.Timeout string", errors.New("Get \"http://localhost/api/v1/vm.info\": context deadline exceeded (Client.Timeout exceeded while awaiting headers)"), true},
		{"connection reset string", errors.New("read: connection reset by peer"), true},
		{"EOF string", errors.New("unexpected EOF in response"), true},
		{"random error", errors.New("something else"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isStopped(tt.err); got != tt.want {
				t.Errorf("isStopped(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// TestStateTimeoutOnHungVMM verifies that State() returns STOPPED within a
// bounded time when the cloud-hypervisor API socket accepts connections but
// never responds (simulating a hung VMM process).
func TestStateTimeoutOnHungVMM(t *testing.T) {
	log := slog.New(slog.NewTextHandler(os.Stdout, nil))
	dataDir := t.TempDir()

	vmm := &VMM{
		dataDir: dataDir,
		log:     log,
	}

	// Create the runtime directory structure and a unix socket that
	// accepts connections but never responds.
	instanceID := "hung-vmm-test"
	runtimeDir := filepath.Join(dataDir, instanceID)
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		t.Fatalf("failed to create runtime dir: %v", err)
	}

	sockPath := vmm.apiSocketPath(instanceID)
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}
	defer ln.Close()

	// Accept connections but never write a response.
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn
		}
	}()

	start := time.Now()
	state, err := vmm.State(context.Background(), instanceID)
	elapsed := time.Since(start)

	// Must not block forever. Allow some margin over stateTimeout.
	if elapsed > stateTimeout+2*time.Second {
		t.Fatalf("State() took %v, expected to timeout around %v", elapsed, stateTimeout)
	}

	// A hung VMM should be reported as STOPPED.
	if state != api.VMState_STOPPED {
		t.Errorf("expected STOPPED, got %v (err: %v)", state, err)
	}
	if err != nil {
		t.Errorf("expected nil error for hung VMM, got: %v", err)
	}
}

// TestStateWithBogusSocket verifies that State() returns STOPPED quickly
// when the API socket path exists as a regular file (not a real socket).
func TestStateWithBogusSocket(t *testing.T) {
	log := slog.New(slog.NewTextHandler(os.Stdout, nil))
	dataDir := t.TempDir()

	vmm := &VMM{
		dataDir: dataDir,
		log:     log,
	}

	instanceID := "bogus-socket-test"
	runtimeDir := filepath.Join(dataDir, instanceID)
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		t.Fatalf("failed to create runtime dir: %v", err)
	}

	// Create a regular file where the socket should be.
	sockPath := vmm.apiSocketPath(instanceID)
	if err := os.WriteFile(sockPath, []byte("not a socket"), 0o644); err != nil {
		t.Fatalf("failed to create bogus socket file: %v", err)
	}

	start := time.Now()
	state, err := vmm.State(context.Background(), instanceID)
	elapsed := time.Since(start)

	// Should fail fast, not block.
	if elapsed > 2*time.Second {
		t.Fatalf("State() took %v, expected fast failure", elapsed)
	}

	if state != api.VMState_STOPPED {
		t.Errorf("expected STOPPED, got %v (err: %v)", state, err)
	}
}
