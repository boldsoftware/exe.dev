package client

import (
	"errors"
	"io/fs"
	"syscall"
	"testing"
)

func TestWrapDialError(t *testing.T) {
	tests := []struct {
		name          string
		err           error
		wantConnected bool // errors.Is(wrapped, ErrNotConnected)
		wantOriginal  bool // errors.Is(wrapped, original)
	}{
		{
			name:          "nil error",
			err:           nil,
			wantConnected: true,
			wantOriginal:  false,
		},
		{
			name:          "fs.ErrNotExist",
			err:           fs.ErrNotExist,
			wantConnected: true,
			wantOriginal:  true,
		},
		{
			name:          "syscall.ECONNREFUSED",
			err:           syscall.ECONNREFUSED,
			wantConnected: true,
			wantOriginal:  true,
		},
		{
			name:          "syscall.ENOENT",
			err:           syscall.ENOENT,
			wantConnected: true,
			wantOriginal:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wrapped := wrapDialError(tt.err)

			if got := errors.Is(wrapped, ErrNotConnected); got != tt.wantConnected {
				t.Errorf("errors.Is(wrapped, ErrNotConnected) = %v, want %v", got, tt.wantConnected)
			}

			if tt.err != nil {
				if got := errors.Is(wrapped, tt.err); got != tt.wantOriginal {
					t.Errorf("errors.Is(wrapped, %v) = %v, want %v", tt.err, got, tt.wantOriginal)
				}
			}
		})
	}
}
