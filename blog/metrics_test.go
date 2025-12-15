package blog

import (
	"html/template"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestNormalizeMetricsPath(t *testing.T) {
	tcs := map[string]string{
		"":                  "/",
		"/":                 "/",
		"/foo":              "/foo",
		"/foo/":             "/foo",
		"/Foo/Bar/":         "/foo/bar",
		"/foo?utm=1":        "/foo",
		"/foo/bar?utm=1#x":  "/foo/bar",
		"foo":               "/foo",
		"foo/":              "/foo",
		"/foo/bar//":        "/foo/bar",
		"/foo/bar//?q=test": "/foo/bar",
	}

	for path, want := range tcs {
		if got := normalizeMetricsPath(path); got != want {
			t.Fatalf("normalizeMetricsPath(%q) = %q; want %q", path, got, want)
		}
	}
}

func TestHandlerRecordsPageHits(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics := NewMetrics(registry)

	now := time.Date(2024, time.February, 2, 0, 0, 0, 0, time.UTC)
	entry := &Entry{
		Path:        "/demo-post",
		Slug:        "demo-post",
		Title:       "Demo",
		Description: "demo",
		Author:      "author",
		Published:   true,
		Date:        now,
		DateString:  now.Format("2006-01-02"),
		Content:     template.HTML("<p>hello</p>"),
	}
	store := &Store{
		entries: []*Entry{entry},
		byPath: map[string]*Entry{
			entry.Path: entry,
		},
		bySlug: map[string]*Entry{
			entry.Slug: entry,
		},
		defaultPath: entry.Path,
		updated:     now,
	}

	blogTmpl, atomTmpl := DefaultTemplates()
	handler := NewHandlerWithTemplates(store, false, blogTmpl, atomTmpl).WithMetrics(metrics)

	req := httptest.NewRequest("GET", "/?utm=1", nil)
	w := httptest.NewRecorder()
	if !handler.Handle(w, req) {
		t.Fatalf("handler did not handle root")
	}
	if got := testutil.ToFloat64(metrics.pageHits.WithLabelValues("/")); got != 1 {
		t.Fatalf("root hits = %v; want 1", got)
	}

	req = httptest.NewRequest("GET", entry.Path+"?ref=1", nil)
	w = httptest.NewRecorder()
	if !handler.Handle(w, req) {
		t.Fatalf("handler did not handle entry")
	}
	if got := testutil.ToFloat64(metrics.pageHits.WithLabelValues(entry.Path)); got != 1 {
		t.Fatalf("entry hits = %v; want 1", got)
	}
}
