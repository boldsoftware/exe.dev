package sshpool2

import (
	"crypto/rand"
	"crypto/rsa"
	"fmt"
	"net"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// testSSHServer creates a minimal SSH server for testing
type testSSHServer struct {
	listener net.Listener
	config   *ssh.ServerConfig
	addr     string
}

func newTestSSHServer(t *testing.T) *testSSHServer {
	// Generate server key
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate server key: %v", err)
	}

	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatalf("failed to create signer: %v", err)
	}

	config := &ssh.ServerConfig{
		NoClientAuth: true, // Accept all connections for testing
	}
	config.AddHostKey(signer)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}

	s := &testSSHServer{
		listener: listener,
		config:   config,
		addr:     listener.Addr().String(),
	}

	go s.serve()

	return s
}

func (s *testSSHServer) serve() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}

		go func(conn net.Conn) {
			_, chans, reqs, err := ssh.NewServerConn(conn, s.config)
			if err != nil {
				conn.Close()
				return
			}

			// Discard all global requests
			go ssh.DiscardRequests(reqs)

			// Accept and service channels
			for newChannel := range chans {
				go func(newChannel ssh.NewChannel) {
					// Accept direct-tcpip channels (used for port forwarding)
					if newChannel.ChannelType() == "direct-tcpip" {
						channel, _, err := newChannel.Accept()
						if err != nil {
							return
						}
						// Just close it immediately for testing
						channel.Close()
					} else {
						newChannel.Reject(ssh.UnknownChannelType, "not supported")
					}
				}(newChannel)
			}
		}(conn)
	}
}

func (s *testSSHServer) close() {
	s.listener.Close()
}

func (s *testSSHServer) host() string {
	host, _, _ := net.SplitHostPort(s.addr)
	return host
}

func (s *testSSHServer) port() int {
	_, portStr, _ := net.SplitHostPort(s.addr)
	port := 0
	fmt.Sscanf(portStr, "%d", &port)
	return port
}

func newTestClientConfig(t *testing.T) (*ssh.ClientConfig, ssh.Signer) {
	// Generate client key
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate client key: %v", err)
	}

	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatalf("failed to create signer: %v", err)
	}

	config := &ssh.ClientConfig{
		User:            "testuser",
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	}

	return config, signer
}

func TestPoolBasicConnection(t *testing.T) {
	server := newTestSSHServer(t)
	defer server.close()

	pool := New(10 * time.Minute)
	defer pool.Close()

	config, signer := newTestClientConfig(t)

	conn, err := pool.Dial("tcp", "127.0.0.1:80", server.host(), config.User, server.port(), signer, config)
	if err != nil {
		t.Fatalf("failed to dial: %v", err)
	}
	defer conn.Close()

	if conn == nil {
		t.Fatal("expected non-nil connection")
	}
}

func TestPoolReuseConnection(t *testing.T) {
	server := newTestSSHServer(t)
	defer server.close()

	pool := New(10 * time.Minute)
	defer pool.Close()

	config, signer := newTestClientConfig(t)

	// Make first dial
	conn1, err := pool.Dial("tcp", "127.0.0.1:80", server.host(), config.User, server.port(), signer, config)
	if err != nil {
		t.Fatalf("failed first dial: %v", err)
	}
	defer conn1.Close()

	// Make second dial - should reuse the same SSH connection
	conn2, err := pool.Dial("tcp", "127.0.0.1:80", server.host(), config.User, server.port(), signer, config)
	if err != nil {
		t.Fatalf("failed second dial: %v", err)
	}
	defer conn2.Close()

	// Check that we only have one SSH connection in the pool
	pool.mu.Lock()
	numConns := len(pool.conns)
	pool.mu.Unlock()

	if numConns != 1 {
		t.Errorf("expected 1 SSH connection in pool, got %d", numConns)
	}
}

func TestPoolDifferentKeys(t *testing.T) {
	server := newTestSSHServer(t)
	defer server.close()

	pool := New(10 * time.Minute)
	defer pool.Close()

	config1, signer1 := newTestClientConfig(t)
	config2, signer2 := newTestClientConfig(t)

	// Dial with first key
	conn1, err := pool.Dial("tcp", "127.0.0.1:80", server.host(), config1.User, server.port(), signer1, config1)
	if err != nil {
		t.Fatalf("failed first dial: %v", err)
	}
	defer conn1.Close()

	// Dial with different key - should create new SSH connection
	conn2, err := pool.Dial("tcp", "127.0.0.1:80", server.host(), config2.User, server.port(), signer2, config2)
	if err != nil {
		t.Fatalf("failed second dial: %v", err)
	}
	defer conn2.Close()

	// Check that we have two SSH connections in the pool
	pool.mu.Lock()
	numConns := len(pool.conns)
	pool.mu.Unlock()

	if numConns != 2 {
		t.Errorf("expected 2 SSH connections in pool, got %d", numConns)
	}
}

func TestPoolExpiration(t *testing.T) {
	server := newTestSSHServer(t)
	defer server.close()

	// Use a very short TTL for testing
	ttl := 100 * time.Millisecond
	pool := New(ttl)
	defer pool.Close()

	config, signer := newTestClientConfig(t)

	// Make first dial
	conn1, err := pool.Dial("tcp", "127.0.0.1:80", server.host(), config.User, server.port(), signer, config)
	if err != nil {
		t.Fatalf("failed first dial: %v", err)
	}
	conn1.Close() // Close to allow connection to expire

	// Wait for expiration (timer should fire)
	time.Sleep(ttl + 50*time.Millisecond)

	// Check that the connection was removed
	pool.mu.Lock()
	numConns := len(pool.conns)
	pool.mu.Unlock()

	if numConns != 0 {
		t.Errorf("expected 0 connections after expiration, got %d", numConns)
	}

	// Make a new dial - should create a new SSH connection
	conn2, err := pool.Dial("tcp", "127.0.0.1:80", server.host(), config.User, server.port(), signer, config)
	if err != nil {
		t.Fatalf("failed dial after expiration: %v", err)
	}
	defer conn2.Close()

	// Check that we have a new connection in the pool
	pool.mu.Lock()
	numConns = len(pool.conns)
	pool.mu.Unlock()

	if numConns != 1 {
		t.Errorf("expected 1 connection after re-dial, got %d", numConns)
	}
}

func TestPoolConcurrentAccess(t *testing.T) {
	server := newTestSSHServer(t)
	defer server.close()

	pool := New(10 * time.Minute)
	defer pool.Close()

	config, signer := newTestClientConfig(t)

	// Launch multiple goroutines trying to dial concurrently
	const numGoroutines = 10
	conns := make([]net.Conn, numGoroutines)
	errs := make([]error, numGoroutines)
	done := make(chan struct{})

	for i := 0; i < numGoroutines; i++ {
		go func(idx int) {
			conns[idx], errs[idx] = pool.Dial("tcp", "127.0.0.1:80", server.host(), config.User, server.port(), signer, config)
			done <- struct{}{}
		}(i)
	}

	// Wait for all goroutines to complete
	for i := 0; i < numGoroutines; i++ {
		<-done
	}

	// Check that all succeeded
	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d failed: %v", i, err)
		}
	}

	// Clean up connections
	for _, conn := range conns {
		if conn != nil {
			conn.Close()
		}
	}

	// Check that we only have one SSH connection in the pool
	pool.mu.Lock()
	numConns := len(pool.conns)
	pool.mu.Unlock()

	if numConns != 1 {
		t.Errorf("expected 1 SSH connection in pool, got %d", numConns)
	}
}

func TestPoolClose(t *testing.T) {
	server := newTestSSHServer(t)
	defer server.close()

	pool := New(10 * time.Minute)

	config, signer := newTestClientConfig(t)

	// Make a dial
	conn, err := pool.Dial("tcp", "127.0.0.1:80", server.host(), config.User, server.port(), signer, config)
	if err != nil {
		t.Fatalf("failed to dial: %v", err)
	}
	conn.Close()

	// Close the pool
	err = pool.Close()
	if err != nil {
		t.Fatalf("failed to close pool: %v", err)
	}

	// Check that all connections were closed
	pool.mu.Lock()
	numConns := len(pool.conns)
	pool.mu.Unlock()

	if numConns != 0 {
		t.Errorf("expected 0 connections after close, got %d", numConns)
	}
}
