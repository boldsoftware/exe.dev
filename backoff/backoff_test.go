package backoff

import (
	"context"
	"errors"
	"testing"
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

func BenchmarkLoopProfile(b *testing.B) {
	b.ReportAllocs()
	var n int
	for range Loop(b.Context(), 1) {
		if n++; n == b.N {
			break
		}
	}
}
