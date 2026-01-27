package resourcemanager

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"exe.dev/exelet/vmm/cloudhypervisor/client"
)

// chainedTestError simulates the client's chained error behavior where
// errors.Is can find both the sentinel (ErrNotConnected) and the underlying cause.
type chainedTestError struct {
	sentinel error
	cause    error
}

func (e *chainedTestError) Error() string {
	return fmt.Sprintf("%v: %v", e.sentinel, e.cause)
}

func (e *chainedTestError) Is(target error) bool {
	return errors.Is(e.sentinel, target) || errors.Is(e.cause, target)
}

func (e *chainedTestError) Unwrap() []error {
	return []error{e.sentinel, e.cause}
}

func TestClassifyVMPIDError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantCode codes.Code
	}{
		{
			name:     "fs.ErrNotExist",
			err:      fs.ErrNotExist,
			wantCode: codes.NotFound,
		},
		{
			name:     "os.ErrNotExist",
			err:      os.ErrNotExist,
			wantCode: codes.NotFound,
		},
		{
			name:     "wrapped fs.ErrNotExist",
			err:      fmt.Errorf("open /run/vm/123/chh.sock: %w", fs.ErrNotExist),
			wantCode: codes.NotFound,
		},
		{
			name:     "deeply wrapped os.ErrNotExist",
			err:      fmt.Errorf("getVMInfo: %w", fmt.Errorf("dial: %w", os.ErrNotExist)),
			wantCode: codes.NotFound,
		},
		{
			name:     "syscall.ENOENT",
			err:      syscall.ENOENT,
			wantCode: codes.NotFound,
		},
		{
			name:     "wrapped syscall.ENOENT",
			err:      fmt.Errorf("dial unix /run/vm/123/chh.sock: %w", syscall.ENOENT),
			wantCode: codes.NotFound,
		},
		{
			name:     "syscall.ECONNREFUSED",
			err:      syscall.ECONNREFUSED,
			wantCode: codes.Unavailable,
		},
		{
			name:     "wrapped ECONNREFUSED",
			err:      fmt.Errorf("dial unix /run/vm/123/chh.sock: %w", syscall.ECONNREFUSED),
			wantCode: codes.Unavailable,
		},
		{
			name:     "client.ErrNotConnected",
			err:      client.ErrNotConnected,
			wantCode: codes.Unavailable,
		},
		{
			name:     "wrapped client.ErrNotConnected",
			err:      fmt.Errorf("unable to connect to api socket: %w", client.ErrNotConnected),
			wantCode: codes.Unavailable,
		},
		{
			name:     "unknown error",
			err:      errors.New("something unexpected"),
			wantCode: codes.Internal,
		},
		{
			name:     "wrapped unknown error",
			err:      fmt.Errorf("getVMInfo: %w", errors.New("parse error")),
			wantCode: codes.Internal,
		},
		// Chained errors: client returns ErrNotConnected wrapping the underlying cause.
		// classifyVMPIDError should detect the root cause (fs.ErrNotExist, ECONNREFUSED)
		// and return the appropriate code.
		{
			name:     "chained ErrNotConnected with fs.ErrNotExist",
			err:      &chainedTestError{sentinel: client.ErrNotConnected, cause: fs.ErrNotExist},
			wantCode: codes.NotFound, // fs.ErrNotExist takes precedence
		},
		{
			name:     "chained ErrNotConnected with syscall.ENOENT",
			err:      &chainedTestError{sentinel: client.ErrNotConnected, cause: syscall.ENOENT},
			wantCode: codes.NotFound, // ENOENT (missing socket) takes precedence
		},
		{
			name:     "chained ErrNotConnected with ECONNREFUSED",
			err:      &chainedTestError{sentinel: client.ErrNotConnected, cause: syscall.ECONNREFUSED},
			wantCode: codes.Unavailable,
		},
		{
			name:     "chained ErrNotConnected with unknown error",
			err:      &chainedTestError{sentinel: client.ErrNotConnected, cause: errors.New("unknown")},
			wantCode: codes.Unavailable, // ErrNotConnected detected
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotErr := classifyVMPIDError(tt.err)
			gotStatus, ok := status.FromError(gotErr)
			if !ok {
				t.Fatalf("classifyVMPIDError returned non-status error: %v", gotErr)
			}
			if gotStatus.Code() != tt.wantCode {
				t.Errorf("classifyVMPIDError(%v) = %v, want %v", tt.err, gotStatus.Code(), tt.wantCode)
			}
		})
	}
}

func TestSetIOMax(t *testing.T) {
	tests := []struct {
		name      string
		majMin    string
		readBPS   uint64
		writeBPS  uint64
		wantValue string
	}{
		{
			name:      "both_limited",
			majMin:    "252:0",
			readBPS:   1048576,
			writeBPS:  524288,
			wantValue: "252:0 rbps=1048576 wbps=524288",
		},
		{
			name:      "read_only_limited",
			majMin:    "252:1",
			readBPS:   2097152,
			writeBPS:  0,
			wantValue: "252:1 rbps=2097152 wbps=max",
		},
		{
			name:      "write_only_limited",
			majMin:    "252:2",
			readBPS:   0,
			writeBPS:  1048576,
			wantValue: "252:2 rbps=max wbps=1048576",
		},
		{
			name:      "both_unlimited",
			majMin:    "252:3",
			readBPS:   0,
			writeBPS:  0,
			wantValue: "252:3 rbps=max wbps=max",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir() // Fresh dir for each subtest
			err := setIOMax(tmpDir, tt.majMin, tt.readBPS, tt.writeBPS)
			if err != nil {
				t.Fatalf("setIOMax() error = %v", err)
			}

			content, err := os.ReadFile(filepath.Join(tmpDir, "io.max"))
			if err != nil {
				t.Fatalf("failed to read io.max: %v", err)
			}

			if string(content) != tt.wantValue {
				t.Errorf("io.max content = %q, want %q", string(content), tt.wantValue)
			}
		})
	}
}

func TestSetIOMaxPreservesOtherDevices(t *testing.T) {
	tmpDir := t.TempDir()

	// Set limit for first device
	if err := setIOMax(tmpDir, "252:0", 1000000, 500000); err != nil {
		t.Fatalf("setIOMax(252:0) error = %v", err)
	}

	// Set limit for second device - should preserve first
	if err := setIOMax(tmpDir, "252:1", 2000000, 1000000); err != nil {
		t.Fatalf("setIOMax(252:1) error = %v", err)
	}

	content, err := os.ReadFile(filepath.Join(tmpDir, "io.max"))
	if err != nil {
		t.Fatalf("failed to read io.max: %v", err)
	}

	// Both devices should be present
	lines := strings.Split(string(content), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d: %q", len(lines), content)
	}

	wantLines := map[string]bool{
		"252:0 rbps=1000000 wbps=500000":  false,
		"252:1 rbps=2000000 wbps=1000000": false,
	}
	for _, line := range lines {
		if _, ok := wantLines[line]; ok {
			wantLines[line] = true
		} else {
			t.Errorf("unexpected line: %q", line)
		}
	}
	for line, found := range wantLines {
		if !found {
			t.Errorf("missing expected line: %q", line)
		}
	}

	// Update first device - should preserve second
	if err := setIOMax(tmpDir, "252:0", 3000000, 1500000); err != nil {
		t.Fatalf("setIOMax(252:0) update error = %v", err)
	}

	content, err = os.ReadFile(filepath.Join(tmpDir, "io.max"))
	if err != nil {
		t.Fatalf("failed to read io.max after update: %v", err)
	}

	lines = strings.Split(string(content), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines after update, got %d: %q", len(lines), content)
	}

	wantLines = map[string]bool{
		"252:0 rbps=3000000 wbps=1500000": false, // Updated
		"252:1 rbps=2000000 wbps=1000000": false, // Preserved
	}
	for _, line := range lines {
		if _, ok := wantLines[line]; ok {
			wantLines[line] = true
		}
	}
	for line, found := range wantLines {
		if !found {
			t.Errorf("missing expected line after update: %q", line)
		}
	}
}

func TestSetIOMaxPreservesIOPS(t *testing.T) {
	tmpDir := t.TempDir()
	ioMaxFile := filepath.Join(tmpDir, "io.max")

	// Pre-populate with a line that has riops/wiops set
	existing := "252:0 rbps=1000000 wbps=500000 riops=1000 wiops=500"
	if err := os.WriteFile(ioMaxFile, []byte(existing), 0o644); err != nil {
		t.Fatalf("failed to write initial io.max: %v", err)
	}

	// Update rbps/wbps - should preserve riops/wiops
	if err := setIOMax(tmpDir, "252:0", 2000000, 1000000); err != nil {
		t.Fatalf("setIOMax() error = %v", err)
	}

	content, err := os.ReadFile(ioMaxFile)
	if err != nil {
		t.Fatalf("failed to read io.max: %v", err)
	}

	want := "252:0 rbps=2000000 wbps=1000000 riops=1000 wiops=500"
	if string(content) != want {
		t.Errorf("io.max content = %q, want %q", string(content), want)
	}
}

func TestClearIOMaxPreservesIOPS(t *testing.T) {
	tmpDir := t.TempDir()
	ioMaxFile := filepath.Join(tmpDir, "io.max")

	// Pre-populate with a line that has riops/wiops set
	existing := "252:0 rbps=1000000 wbps=500000 riops=1000 wiops=500"
	if err := os.WriteFile(ioMaxFile, []byte(existing), 0o644); err != nil {
		t.Fatalf("failed to write initial io.max: %v", err)
	}

	// Clear rbps/wbps - should preserve riops/wiops
	if err := clearIOMax(tmpDir, "252:0"); err != nil {
		t.Fatalf("clearIOMax() error = %v", err)
	}

	content, err := os.ReadFile(ioMaxFile)
	if err != nil {
		t.Fatalf("failed to read io.max: %v", err)
	}

	want := "252:0 rbps=max wbps=max riops=1000 wiops=500"
	if string(content) != want {
		t.Errorf("io.max content = %q, want %q", string(content), want)
	}
}

func TestClearIOMax(t *testing.T) {
	tmpDir := t.TempDir()

	err := clearIOMax(tmpDir, "252:0")
	if err != nil {
		t.Fatalf("clearIOMax() error = %v", err)
	}

	content, err := os.ReadFile(filepath.Join(tmpDir, "io.max"))
	if err != nil {
		t.Fatalf("failed to read io.max: %v", err)
	}

	want := "252:0 rbps=max wbps=max"
	if string(content) != want {
		t.Errorf("io.max content = %q, want %q", string(content), want)
	}
}

func TestGetDeviceMajorMinor(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("skipping test on non-Linux platform")
	}

	// Test with /dev/null which exists on all Linux systems
	majMin, err := getDeviceMajorMinor("/dev/null")
	if err != nil {
		t.Fatalf("getDeviceMajorMinor(/dev/null) error = %v", err)
	}

	// /dev/null is typically 1:3 on Linux
	if majMin != "1:3" {
		t.Errorf("getDeviceMajorMinor(/dev/null) = %q, want 1:3", majMin)
	}
}

func TestGetDeviceMajorMinor_NotExist(t *testing.T) {
	_, err := getDeviceMajorMinor("/dev/nonexistent-device-12345")
	if err == nil {
		t.Fatal("getDeviceMajorMinor() expected error for nonexistent device")
	}
}
