package exelet

import (
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"exe.dev/exelet/config"
	"exe.dev/stage"
)

func TestHTTPServer(t *testing.T) {
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	cfg := &config.ExeletConfig{
		Name:          "test",
		ListenAddress: "127.0.0.1:0", // random port for grpc
		DataDir:       t.TempDir(),
	}

	registry := prometheus.NewRegistry()
	srv, err := NewExelet(cfg, log, stage.Test(), WithMetricsRegistry(registry))
	if err != nil {
		t.Fatalf("failed to create exelet: %v", err)
	}

	// Use port 0 for tests to avoid collisions
	if _, err := srv.StartHTTPServer("127.0.0.1:0", srv.MetricsRegistry()); err != nil {
		t.Fatalf("failed to start HTTP server: %v", err)
	}

	registry2 := prometheus.NewRegistry()
	srv2, err := NewExelet(cfg, log, stage.Test(), WithMetricsRegistry(registry2))
	if err != nil {
		t.Fatalf("failed to create exelet: %v", err)
	}
	actualAddr, err := srv2.StartHTTPServer("127.0.0.1:0", srv2.MetricsRegistry())
	if err != nil {
		t.Fatalf("failed to start HTTP server: %v", err)
	}

	baseURL := "http://" + actualAddr

	// Test version endpoint
	t.Run("version", func(t *testing.T) {
		resp, err := http.Get(baseURL + "/debug/version")
		if err != nil {
			t.Fatalf("failed to get version: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("expected status 200, got %d", resp.StatusCode)
		}

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("failed to read body: %v", err)
		}

		if !strings.Contains(string(body), "exe/") {
			t.Errorf("expected version info in body, got: %s", body)
		}
	})

	// Test metrics endpoint
	t.Run("metrics", func(t *testing.T) {
		resp, err := http.Get(baseURL + "/metrics")
		if err != nil {
			t.Fatalf("failed to get metrics: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("expected status 200, got %d", resp.StatusCode)
		}

		// Just verify the endpoint is accessible
		// TODO: add actual metrics when grpc-middleware is integrated
	})

	// Test pprof endpoint
	t.Run("pprof", func(t *testing.T) {
		resp, err := http.Get(baseURL + "/debug/pprof/")
		if err != nil {
			t.Fatalf("failed to get pprof: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("expected status 200, got %d", resp.StatusCode)
		}

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("failed to read body: %v", err)
		}

		// Check for pprof content
		if !strings.Contains(string(body), "heap") {
			t.Errorf("expected pprof heap in body")
		}
	})

	// Test root redirect
	t.Run("root_redirect", func(t *testing.T) {
		client := &http.Client{
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse // don't follow redirects
			},
		}
		resp, err := client.Get(baseURL + "/")
		if err != nil {
			t.Fatalf("failed to get root: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusFound {
			t.Errorf("expected status 302, got %d", resp.StatusCode)
		}

		location := resp.Header.Get("Location")
		if location != "/debug" {
			t.Errorf("expected redirect to /debug, got %s", location)
		}
	})

	// Test debug index
	t.Run("debug_index", func(t *testing.T) {
		resp, err := http.Get(baseURL + "/debug")
		if err != nil {
			t.Fatalf("failed to get debug index: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("expected status 200, got %d", resp.StatusCode)
		}

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("failed to read body: %v", err)
		}

		// Check for expected links
		if !strings.Contains(string(body), "pprof") {
			t.Errorf("expected pprof link in debug index")
		}
		if !strings.Contains(string(body), "version") {
			t.Errorf("expected version link in debug index")
		}
		if !strings.Contains(string(body), "metrics") {
			t.Errorf("expected metrics link in debug index")
		}
	})
}

func TestMetrics(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics := NewExeletMetrics(registry)

	if metrics == nil {
		t.Fatal("expected metrics to be created")
	}

	// Gather metrics to ensure registry is working
	_, err := registry.Gather()
	if err != nil {
		t.Fatalf("failed to gather metrics: %v", err)
	}

	// TODO: verify actual metrics once grpc-middleware is integrated
}
