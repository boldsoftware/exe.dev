package exe

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"exe.dev/testutil"
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

	name := strings.ReplaceAll(t.Name(), "/", "_") // unique in-memory sqlite database per test
	dsn := fmt.Sprintf("file:%s-%d?mode=memory&cache=shared", name, time.Now().UnixNano())

	s, err := NewServer(testutil.Slogger(t), ":0", ":0", ":0", ":0", dsn, "test", "", 2222, "ghuser/whoami.sqlite3", dockerhosts)
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}
	t.Cleanup(func() { s.Stop() }) // Ensure server is stopped when test ends (even if not started)
	return s
}
