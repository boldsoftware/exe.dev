package execore

import (
	"path/filepath"
	"testing"

	"exe.dev/stage"
	"exe.dev/tslog"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	s := newUnstartedServer(t)
	s.startAndAwaitReady()
	return s
}

// newHTTPOnlyTestServer creates a test server with only HTTP (no HTTPS/TLS/Cobble/Tailscale).
// This is much faster for tests that don't need TLS functionality.
func newHTTPOnlyTestServer(t *testing.T) *Server {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.sqlite3")
	env := stage.Local()
	env.DevMode = "test"
	env.UseCobble = false // Disable Cobble/Pebble ACME server
	// Pass empty string for httpsAddr to skip HTTPS setup entirely
	s, err := NewServer(tslog.Slogger(t), ":0", "", ":0", ":0", dbPath, "test", "", 2222, "", nil, "", env)
	if err != nil {
		t.Fatalf("failed to create HTTP-only server: %v", err)
	}
	t.Cleanup(func() { s.Stop() })
	s.startAndAwaitReady()
	return s
}

func (s *Server) startAndAwaitReady() {
	go s.Start()
	s.ready.Wait()
}

func newUnstartedServer(t testing.TB) *Server {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.sqlite3")
	env := stage.Local()
	env.DevMode = "test"
	s, err := NewServer(tslog.Slogger(t), ":0", ":0", ":0", ":0", dbPath, "test", "", 2222, "", nil, "", env)
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}
	t.Cleanup(func() { s.Stop() }) // Ensure server is stopped when test ends (even if not started)
	return s
}

// BenchmarkNewTestServer benchmarks the creation of a new test server.
// This is directly proportional to the time it takes to run these tests, which is an ongoing pain point.
func BenchmarkNewTestServer(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		s := newUnstartedServer(b)
		s.startAndAwaitReady()
		s.Stop()
	}
}
