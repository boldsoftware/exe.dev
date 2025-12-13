package execore

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"exe.dev/metricsbag"
)

func TestHTTPMetricsNonProxy(t *testing.T) {
	registry := prometheus.NewRegistry()
	m := NewHTTPMetrics(registry)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		metricsbag.SetLabel(r.Context(), LabelProxy, "false")
		metricsbag.SetLabel(r.Context(), LabelPath, r.URL.Path)
		switch r.URL.Path {
		case "/auth":
			w.WriteHeader(http.StatusUnauthorized)
		case "/notfound":
			w.WriteHeader(http.StatusNotFound)
		default:
			w.WriteHeader(http.StatusOK)
		}
	})

	wrapped := metricsbag.Wrap(m.Wrap(handler))

	makeRequest := func(path string) {
		req := httptest.NewRequest("GET", path, nil)
		rec := httptest.NewRecorder()
		wrapped.ServeHTTP(rec, req)
	}

	makeRequest("/")
	makeRequest("/")
	makeRequest("/health")
	makeRequest("/auth")
	makeRequest("/notfound")

	expected := `
		# HELP http_requests_total Total number of HTTP requests.
		# TYPE http_requests_total counter
		http_requests_total{box="",code="200",path="/",proxy="false"} 2
		http_requests_total{box="",code="200",path="/health",proxy="false"} 1
		http_requests_total{box="",code="401",path="/auth",proxy="false"} 1
		http_requests_total{box="",code="404",path="/notfound",proxy="false"} 1
	`
	if err := testutil.CollectAndCompare(m.requestsTotal, strings.NewReader(expected)); err != nil {
		t.Errorf("http_requests_total mismatch: %v", err)
	}
}

func TestHTTPMetricsProxy(t *testing.T) {
	registry := prometheus.NewRegistry()
	m := NewHTTPMetrics(registry)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		metricsbag.SetLabel(r.Context(), LabelProxy, "true")
		boxName := r.Header.Get("X-Box-Name")
		metricsbag.SetLabel(r.Context(), LabelBox, boxName)
		switch boxName {
		case "errorbox":
			w.WriteHeader(http.StatusBadGateway)
		case "badbox":
			w.WriteHeader(http.StatusBadRequest)
		default:
			w.WriteHeader(http.StatusOK)
		}
	})

	wrapped := metricsbag.Wrap(m.Wrap(handler))

	makeRequest := func(boxName string) {
		req := httptest.NewRequest("GET", "/", nil)
		req.Header.Set("X-Box-Name", boxName)
		rec := httptest.NewRecorder()
		wrapped.ServeHTTP(rec, req)
	}

	makeRequest("mybox")
	makeRequest("mybox")
	makeRequest("errorbox")
	makeRequest("otherbox")
	makeRequest("badbox")

	expected := `
		# HELP http_requests_total Total number of HTTP requests.
		# TYPE http_requests_total counter
		http_requests_total{box="badbox",code="400",path="",proxy="true"} 1
		http_requests_total{box="errorbox",code="502",path="",proxy="true"} 1
		http_requests_total{box="mybox",code="200",path="",proxy="true"} 2
		http_requests_total{box="otherbox",code="200",path="",proxy="true"} 1
	`
	if err := testutil.CollectAndCompare(m.requestsTotal, strings.NewReader(expected)); err != nil {
		t.Errorf("http_requests_total mismatch: %v", err)
	}
}

func TestNormalizePath(t *testing.T) {
	tests := []struct {
		path     string
		expected string
	}{
		{"/", "/"},
		{"", "/"},
		{"/foo", "/foo"},
		{"/foo/", "/foo"},
		{"/foo/bar", "/foo/bar"},
		{"/foo/bar/", "/foo/bar"},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			result := normalizePath(tt.path)
			if result != tt.expected {
				t.Errorf("normalizePath(%q) = %q, want %q", tt.path, result, tt.expected)
			}
		})
	}
}
