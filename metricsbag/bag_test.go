package metricsbag

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLabelBag(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		SetLabel(r.Context(), "foo", "bar")
		SetLabel(r.Context(), "baz", "qux")

		fooFn := LabelFromCtx("foo")
		bazFn := LabelFromCtx("baz")
		unknownFn := LabelFromCtx("unknown")

		if v := fooFn(r.Context()); v != "bar" {
			t.Errorf("expected foo=bar, got %q", v)
		}
		if v := bazFn(r.Context()); v != "qux" {
			t.Errorf("expected baz=qux, got %q", v)
		}
		if v := unknownFn(r.Context()); v != "" {
			t.Errorf("expected empty string for unknown label, got %q", v)
		}

		w.WriteHeader(http.StatusOK)
	})

	wrapped := Wrap(handler)

	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}
}

func TestSetLabelWithoutBag(t *testing.T) {
	ctx := context.Background()
	SetLabel(ctx, "foo", "bar") // Should not panic

	fn := LabelFromCtx("foo")
	if v := fn(ctx); v != "" {
		t.Errorf("expected empty string without bag, got %q", v)
	}
}
