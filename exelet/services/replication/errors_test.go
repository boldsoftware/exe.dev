package replication

import (
	"errors"
	"fmt"
	"testing"
)

func TestIsOutOfSpaceErr(t *testing.T) {
	cases := []struct {
		name string
		msgs []string
		want bool
	}{
		{"empty", nil, false},
		{"unrelated", []string{"connection refused"}, false},
		{"recv out of space", []string{"cannot receive: out of space"}, true},
		{"linux ENOSPC", []string{"write: No space left on device"}, true},
		{"dataset is full", []string{"the dataset is full"}, true},
		{"quota exceeded", []string{"Disc quota exceeded by user"}, true},
		{"mixed-case match", []string{"OUT OF SPACE"}, true},
		{"second arg matches", []string{"unrelated", "out of space here"}, true},
		{"empty strings ignored", []string{"", ""}, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isOutOfSpaceErr(tc.msgs...); got != tc.want {
				t.Errorf("isOutOfSpaceErr(%v) = %v, want %v", tc.msgs, got, tc.want)
			}
		})
	}
}

func TestClassifySendErr(t *testing.T) {
	t.Run("nil passes through", func(t *testing.T) {
		if got := classifySendErr(nil, "out of space"); got != nil {
			t.Errorf("expected nil, got %v", got)
		}
	})

	t.Run("benign error untouched", func(t *testing.T) {
		base := errors.New("connection refused")
		got := classifySendErr(base, "")
		if !errors.Is(got, base) {
			t.Errorf("expected base error to be preserved, got %v", got)
		}
		if errors.Is(got, ErrTargetFull) {
			t.Errorf("benign error should not be classified as ErrTargetFull")
		}
	})

	t.Run("ENOSPC in stderr wraps with ErrTargetFull", func(t *testing.T) {
		base := errors.New("remote zfs recv failed: exit status 1")
		got := classifySendErr(base, "cannot receive new filesystem stream: out of space")
		if !errors.Is(got, ErrTargetFull) {
			t.Errorf("expected ErrTargetFull wrap, got %v", got)
		}
		if !errors.Is(got, base) {
			t.Errorf("expected original error to remain wrapped, got %v", got)
		}
	})

	t.Run("ENOSPC in error message wraps with ErrTargetFull", func(t *testing.T) {
		base := fmt.Errorf("write failed: %w", errors.New("no space left on device"))
		got := classifySendErr(base)
		if !errors.Is(got, ErrTargetFull) {
			t.Errorf("expected ErrTargetFull wrap, got %v", got)
		}
	})

	t.Run("already-classified error not double-wrapped", func(t *testing.T) {
		base := fmt.Errorf("%w: x", ErrTargetFull)
		got := classifySendErr(base, "out of space")
		if got != base {
			t.Errorf("expected already-wrapped error to pass through unchanged")
		}
	})
}
