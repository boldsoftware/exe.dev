package metricsbag

import (
	"context"
	"net/http"
	"sync"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type labelBag struct {
	mu sync.Mutex
	m  map[string]string
}

type bagKey struct{}

// Wrap wraps a handler to provide a label bag in the request context.
// Use SetLabel to set label values from anywhere in the handler chain,
// and LabelFromCtx to extract them for promhttp instrumentation.
//
// This follows a similar pattern from sloghttp, and creates a mutable
// Context. We want to extract labels during the processing of various handlers,
// so we want them to emit what the label is (e.g., what the box name is)
// during processing. This is ultimately a mutable bag that can add things
// to Context without entirely wrapping.
func Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := &labelBag{m: make(map[string]string)}
		ctx := context.WithValue(r.Context(), bagKey{}, b)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// SetLabel sets a label value in the context's label bag.
func SetLabel(ctx context.Context, k, v string) {
	if b, ok := ctx.Value(bagKey{}).(*labelBag); ok && b != nil {
		b.mu.Lock()
		b.m[k] = v
		b.mu.Unlock()
	}
}

// LabelFromCtx returns a promhttp.LabelValueFromCtx function that extracts
// the named label from the context's label bag. Returns empty string if not set.
func LabelFromCtx(name string) promhttp.LabelValueFromCtx {
	return func(ctx context.Context) string {
		if b, ok := ctx.Value(bagKey{}).(*labelBag); ok && b != nil {
			b.mu.Lock()
			v := b.m[name]
			b.mu.Unlock()
			return v
		}
		return ""
	}
}
