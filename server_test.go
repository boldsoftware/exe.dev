package exe

import (
	"os"
	"testing"
)

func NewTestServer(t *testing.T, dockerhosts ...string) *Server {
	t.Helper()

	tmpDB, err := os.CreateTemp("", t.Name()+"_*.db")
	if err != nil {
		t.Fatalf("failed to create temp db: %v", err)
	}
	t.Cleanup(func() {
		tmpDB.Close()
		os.Remove(tmpDB.Name())
	})

	if len(dockerhosts) == 0 {
		dockerhosts = []string{""}
	}

	server, err := NewServer(":0", "", ":0", ":0", tmpDB.Name(), "test", "", 2222, dockerhosts)
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}
	t.Cleanup(func() {
		server.Stop()
	})

	server.testMode = true


	go server.Start()
	server.ready.Wait()

	return server
}
