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

func TestProxyBytesMetric(t *testing.T) {
	registry := prometheus.NewRegistry()
	m := NewHTTPMetrics(registry)

	// Simulate bytes being tracked
	m.AddProxyBytes("in", 100)
	m.AddProxyBytes("in", 50)
	m.AddProxyBytes("out", 200)
	m.AddProxyBytes("out", 75)

	expected := `
		# HELP proxy_bytes_total Total number of bytes proxied.
		# TYPE proxy_bytes_total counter
		proxy_bytes_total{direction="in"} 150
		proxy_bytes_total{direction="out"} 275
	`
	if err := testutil.CollectAndCompare(m.proxyBytesTotal, strings.NewReader(expected)); err != nil {
		t.Errorf("proxy_bytes_total mismatch: %v", err)
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

func TestGitBuildInfo(t *testing.T) {
	registry := prometheus.NewRegistry()
	RegisterBuildInfo(registry)

	metrics, err := registry.Gather()
	if err != nil {
		t.Fatalf("failed to gather metrics: %v", err)
	}

	found := false
	for _, m := range metrics {
		if m.GetName() == "git_build_info" {
			found = true
			if len(m.GetMetric()) == 0 {
				t.Fatal("git_build_info has no metrics")
			}
			metric := m.GetMetric()[0]
			hasCommit := false
			for _, label := range metric.GetLabel() {
				if label.GetName() == "commit" {
					hasCommit = true
					if label.GetValue() == "" {
						t.Error("commit label is empty")
					}
					t.Logf("commit label value: %s", label.GetValue())
				}
			}
			if !hasCommit {
				t.Error("git_build_info missing commit label")
			}
			if metric.GetGauge().GetValue() != 1 {
				t.Errorf("git_build_info value = %v, want 1", metric.GetGauge().GetValue())
			}
		}
	}

	if !found {
		t.Error("git_build_info metric not found")
	}
}

func TestEntityMetrics(t *testing.T) {
	// Use a test server which sets up the database with migrations
	// Note: s.Stop() is called by the test helper's cleanup
	s := newTestServer(t)

	// Gather metrics from the server's registry
	metrics, err := s.metricsRegistry.Gather()
	if err != nil {
		t.Fatalf("failed to gather metrics: %v", err)
	}

	loginUsersFound := false
	devUsersFound := false
	vmsFound := false
	usersWithVMsFound := false
	for _, m := range metrics {
		switch m.GetName() {
		case "users_total":
			for _, metric := range m.GetMetric() {
				for _, label := range metric.GetLabel() {
					if label.GetName() == "type" {
						switch label.GetValue() {
						case "login":
							loginUsersFound = true
							if v := metric.GetGauge().GetValue(); v != 0 {
								t.Errorf("users_total{type=login} = %v, want 0", v)
							}
						case "dev":
							devUsersFound = true
							if v := metric.GetGauge().GetValue(); v != 0 {
								t.Errorf("users_total{type=dev} = %v, want 0", v)
							}
						}
					}
				}
			}
		case "vms_total":
			vmsFound = true
			if v := m.GetMetric()[0].GetGauge().GetValue(); v != 0 {
				t.Errorf("vms_total = %v, want 0", v)
			}
		case "users_with_vms_total":
			usersWithVMsFound = true
			if v := m.GetMetric()[0].GetGauge().GetValue(); v != 0 {
				t.Errorf("users_with_vms_total = %v, want 0", v)
			}
		}
	}

	if !loginUsersFound {
		t.Error("users_total{type=login} metric not found")
	}
	if !devUsersFound {
		t.Error("users_total{type=dev} metric not found")
	}
	if !vmsFound {
		t.Error("vms_total metric not found")
	}
	if !usersWithVMsFound {
		t.Error("users_with_vms_total metric not found")
	}
}
