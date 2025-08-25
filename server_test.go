package exe

import (
	"os"
	"testing"
)

func NewTestServer(t *testing.T, httpAddr, sshAddr string) *Server {
	t.Helper()

	tmpDB, err := os.CreateTemp("", t.Name()+"_*.db")
	if err != nil {
		t.Fatalf("failed to create temp db: %v", err)
	}
	t.Cleanup(func() {
		tmpDB.Close()
		os.Remove(tmpDB.Name())
	})

	server, err := NewServer(httpAddr, "", sshAddr, ":0", tmpDB.Name(), "local", []string{""})
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}
	t.Cleanup(func() {
		server.Stop()
	})

	return server
}
