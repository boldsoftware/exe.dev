package exe

import (
	"path/filepath"
	"testing"
)

func NewTestServer(t *testing.T, dockerhosts ...string) *Server {
	t.Helper()
	s := newUnstartedServer(t, dockerhosts...)
	s.startAndAwaitReady()
	return s
}

func (s *Server) startAndAwaitReady() {
	go s.Start()
	s.ready.Wait()
}

func newUnstartedServer(t *testing.T, dockerhosts ...string) *Server {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.sqlite3")
	s, err := NewServer(":0", ":0", ":0", ":0", dbPath, "test", "", 2222, "ghuser/whoami.sqlite3", dockerhosts)
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}
	t.Cleanup(func() { s.Stop() }) // Ensure server is stopped when test ends (even if not started)
	return s
}
