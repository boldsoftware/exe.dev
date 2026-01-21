package resourcemanager

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
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
