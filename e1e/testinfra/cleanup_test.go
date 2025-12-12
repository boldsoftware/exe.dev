package testinfra

import (
	"testing"
)

func TestCleanups(t *testing.T) {
	count := 0

	fn := func() { count++ }

	AddCleanup(fn)
	AddCleanup(fn)

	wantCount := func(want int) {
		if count != want {
			t.Helper()
			t.Errorf("count is %d want %d", count, want)
		}
	}

	wantCount(0)

	RunCleanups()

	wantCount(2)

	RunCleanups()
	wantCount(2)
}
