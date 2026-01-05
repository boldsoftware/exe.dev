package backoff

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestLoop(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	var n int
	for err := range Loop(ctx, 1) {
		if !errors.Is(err, ctx.Err()) {
			t.Errorf("err = %v, want %v", err, ctx.Err())
		}
		if err != nil {
			break
		}
		if n++; n > 5 {
			cancel()
		}
	}
}

// timerPoolOwner is used to ensure that fixTimerPool is not called more than
// once per test.
var timerPoolOwner *testing.T

// fixTimerPool replaces the timer pool's New function to return
// a single reusable timer, ensuring that no allocations occur.
func fixTimerPool(t *testing.T) (cleanup func()) {
	t.Chdir(".") // ensure we are no runnign in parallel
	if timerPoolOwner != nil {
		panic(fmt.Sprintf("fixTimerPool called from %v, already owned by %v", t.Name(), timerPoolOwner.Name()))
	}
	reuse := time.NewTimer(0)
	oldNew := timerPool.New
	timerPool.New = func() any { return reuse }
	timerPoolOwner = t
	cleanup = func() {
		timerPool.New = oldNew
		timerPoolOwner = nil
	}
	t.Cleanup(cleanup)
	return cleanup
}

func TestLoopAllocs(t *testing.T) {
	fixTimerPool(t)

	got := testing.AllocsPerRun(1000, func() {
		tick := 0
		for range Loop(t.Context(), 1) {
			if tick++; tick > 10 {
				break
			}
		}
	})

	if got > 0 {
		t.Errorf("unexpected allocations: %v > 0", got)
	}
}

func BenchmarkLoopProfile(b *testing.B) {
	b.ReportAllocs()
	var n int
	for range Loop(b.Context(), 1) {
		if n++; n == b.N {
			break
		}
	}
}
