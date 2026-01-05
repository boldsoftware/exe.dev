package backoff

import (
	"context"
	"iter"
	"math/rand/v2"
	"sync"
	"time"
)

var timerPool = &sync.Pool{
	New: func() any {
		return time.NewTimer(0)
	},
}

func putTimer(t *time.Timer) {
	if t == nil {
		return
	}
	t.Stop()
	timerPool.Put(t)
}

// Loop returns a sequence that yields nil until the context is cancelled,
// or the sequence is stopped, which ever comes first, with an increasing
// backoff between yields.
//
// If the context is cancelled before the first yield,
// the sequence yields exactly once with the context's error.
func Loop(ctx context.Context, maxBackoff time.Duration) iter.Seq[error] {
	var n int
	return func(yield func(error) bool) {
		var t *time.Timer
		defer putTimer(t)

		for {
			// The context may be cancelled right as we wake up.
			// If so, prevent starting a new operation.
			if ctx.Err() != nil {
				yield(ctx.Err())
				return
			}

			if !yield(nil) {
				return
			}

			n++

			// n^2 backoff timer is a little smoother than the
			// common choice of 2^n.
			d := time.Duration(n*n) * 10 * time.Millisecond

			// Cap the backoff to a "maximum" value.
			// We still add jitter to avoid synchronized retries,
			// so the actual delay may a smidgen more than this.
			d = min(d, maxBackoff)

			// Randomize the delay between 0.5-1.5 x msec, in order
			// to prevent accidental "thundering herd" problems.
			d = time.Duration(float64(d) * (rand.Float64() + 0.5))

			if t == nil {
				t = timerPool.Get().(*time.Timer)
			}
			t.Reset(d)
			select {
			case <-ctx.Done():
				t.Stop()
			case <-t.C:
			}
		}
	}
}
