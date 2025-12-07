package execore

import (
	"path/filepath"
	"testing"

	"exe.dev/stage"
	"exe.dev/tslog"
	"github.com/prometheus/client_golang/prometheus"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	s := newUnstartedServer(t)
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
	env := stage.Test()
	registry := prometheus.NewRegistry()
	s, err := NewServer(tslog.Slogger(t), ":0", ":0", ":0", ":0", dbPath, "", 2222, "", nil, "", env, registry)
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
