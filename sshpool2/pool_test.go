package sshpool2

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"fmt"
	"io"
	mathrand "math/rand"
	"net"
	"strconv"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"exe.dev/tslog"
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
	port, _ := strconv.Atoi(portStr)
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

func mustCloseConn(t *testing.T, conn net.Conn) {
	t.Helper()
	if err := conn.Close(); err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
		t.Fatalf("close failed: %v", err)
	}
}

func getOnlyPooledConn(t *testing.T, pool *Pool) *pooledConn {
	t.Helper()

	pool.mu.Lock()
	defer pool.mu.Unlock()

	if len(pool.conns) != 1 {
		t.Fatalf("expected 1 pooled connection, got %d", len(pool.conns))
	}
	for _, pc := range pool.conns {
		return pc
	}
	t.Fatal("no pooled connection found")
	return nil
}

func TestPoolBasicConnection(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		server := newTestSSHServer(t)
		defer server.close()

		pool := &Pool{TTL: 10 * time.Minute, Logger: tslog.Slogger(t)}
		defer pool.Close()

		config, signer := newTestClientConfig(t)

		conn, err := pool.DialContext(t.Context(), "tcp", "127.0.0.1:80", server.host(), config.User, server.port(), signer, config)
		if err != nil {
			t.Fatalf("failed to dial: %v", err)
		}
		defer conn.Close()

		if conn == nil {
			t.Fatal("expected non-nil connection")
		}
	})
}

func TestPooledConnDisconnectedResetsActive(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		server := newTestSSHServer(t)
		defer server.close()

		pool := &Pool{TTL: time.Minute, Logger: tslog.Slogger(t)}
		defer pool.Close()

		config, signer := newTestClientConfig(t)

		conn, err := pool.DialContext(t.Context(), "tcp", "127.0.0.1:80", server.host(), config.User, server.port(), signer, config)
		if err != nil {
			t.Fatalf("failed initial dial: %v", err)
		}
		pc := getOnlyPooledConn(t, pool)

		mustCloseConn(t, conn)

		pc.mu.Lock()
		if got := pc.active; got != 1 {
			t.Fatalf("after initial close: active=%d, want 1", got)
		}
		pc.mu.Unlock()

		for i := 0; i < 3; i++ {
			conn, err := pool.DialContext(t.Context(), "tcp", "127.0.0.1:80", server.host(), config.User, server.port(), signer, config)
			if err != nil {
				t.Fatalf("iteration %d dial failed: %v", i, err)
			}
			mustCloseConn(t, conn)

			pc.mu.Lock()
			if got := pc.active; got != 1 {
				t.Fatalf("iteration %d: active=%d, want 1", i, got)
			}
			if pc.timer == nil {
				t.Fatalf("iteration %d: timer is nil", i)
			}
			pc.mu.Unlock()
		}
	})
}

func TestPooledConnTimersReleaseOnce(t *testing.T) {
	server := newTestSSHServer(t)
	defer server.close()

	pool := &Pool{TTL: 25 * time.Millisecond, Logger: tslog.Slogger(t)}
	defer pool.Close()

	config, signer := newTestClientConfig(t)

	conn, err := pool.DialContext(t.Context(), "tcp", "127.0.0.1:80", server.host(), config.User, server.port(), signer, config)
	if err != nil {
		t.Fatalf("failed initial dial: %v", err)
	}
	pc := getOnlyPooledConn(t, pool)

	mustCloseConn(t, conn)

	for i := 0; i < 2; i++ {
		conn, err := pool.DialContext(t.Context(), "tcp", "127.0.0.1:80", server.host(), config.User, server.port(), signer, config)
		if err != nil {
			t.Fatalf("iteration %d dial failed: %v", i, err)
		}
		mustCloseConn(t, conn)
	}

	pc.mu.Lock()
	if got := pc.active; got != 1 {
		pc.mu.Unlock()
		t.Fatalf("before TTL expiry: active=%d, want 1", got)
	}
	pc.mu.Unlock()

	deadline := time.Now().Add(2 * time.Second)

	for {
		pool.mu.Lock()
		_, stillPresent := pool.conns[pc.key]
		pool.mu.Unlock()

		if !stillPresent {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("pool entry still present after %v", time.Since(deadline.Add(-2*time.Second)))
		}
		time.Sleep(5 * time.Millisecond)
	}

	for {
		pc.mu.Lock()
		active := pc.active
		pc.mu.Unlock()
		if active == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("pooledConn.active still %d after release deadline", active)
		}
		time.Sleep(5 * time.Millisecond)
	}

	conn, err = pool.DialContext(t.Context(), "tcp", "127.0.0.1:80", server.host(), config.User, server.port(), signer, config)
	if err != nil {
		t.Fatalf("dial after release failed: %v", err)
	}
	mustCloseConn(t, conn)
}

func TestPooledConnActiveCountsWithParallelUse(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		server := newTestSSHServer(t)
		defer server.close()

		pool := &Pool{TTL: time.Minute, Logger: tslog.Slogger(t)}
		defer pool.Close()

		config, signer := newTestClientConfig(t)

		conn1, err := pool.DialContext(t.Context(), "tcp", "127.0.0.1:80", server.host(), config.User, server.port(), signer, config)
		if err != nil {
			t.Fatalf("first dial failed: %v", err)
		}
		pc := getOnlyPooledConn(t, pool)

		conn2, err := pool.DialContext(t.Context(), "tcp", "127.0.0.1:80", server.host(), config.User, server.port(), signer, config)
		if err != nil {
			t.Fatalf("second dial failed: %v", err)
		}

		pc.mu.Lock()
		if got := pc.active; got != 3 {
			t.Fatalf("with two open connections: active=%d, want 3", got)
		}
		pc.mu.Unlock()

		mustCloseConn(t, conn1)

		pc.mu.Lock()
		if got := pc.active; got != 2 {
			t.Fatalf("after closing first connection: active=%d, want 2", got)
		}
		pc.mu.Unlock()

		mustCloseConn(t, conn2)

		pc.mu.Lock()
		if got := pc.active; got != 1 {
			t.Fatalf("after closing second connection: active=%d, want 1", got)
		}
		if pc.timer == nil {
			t.Fatal("timer should remain set after closing second connection")
		}
		pc.mu.Unlock()
	})
}

func TestPoolReuseConnection(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		server := newTestSSHServer(t)
		defer server.close()

		pool := &Pool{TTL: 10 * time.Minute, Logger: tslog.Slogger(t)}
		defer pool.Close()

		config, signer := newTestClientConfig(t)

		// Make first dial
		conn1, err := pool.DialContext(t.Context(), "tcp", "127.0.0.1:80", server.host(), config.User, server.port(), signer, config)
		if err != nil {
			t.Fatalf("failed first dial: %v", err)
		}
		defer conn1.Close()

		// Make second dial - should reuse the same SSH connection
		conn2, err := pool.DialContext(t.Context(), "tcp", "127.0.0.1:80", server.host(), config.User, server.port(), signer, config)
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
	})
}

func TestPoolDifferentKeys(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		server := newTestSSHServer(t)
		defer server.close()

		pool := &Pool{TTL: 10 * time.Minute, Logger: tslog.Slogger(t)}
		defer pool.Close()

		config1, signer1 := newTestClientConfig(t)
		config2, signer2 := newTestClientConfig(t)

		// Dial with first key
		conn1, err := pool.DialContext(t.Context(), "tcp", "127.0.0.1:80", server.host(), config1.User, server.port(), signer1, config1)
		if err != nil {
			t.Fatalf("failed first dial: %v", err)
		}
		defer conn1.Close()

		// Dial with different key - should create new SSH connection
		conn2, err := pool.DialContext(t.Context(), "tcp", "127.0.0.1:80", server.host(), config2.User, server.port(), signer2, config2)
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
	})
}

func TestPoolExpiration(t *testing.T) {
	// synctest.Test(t, func(t *testing.T) {
	server := newTestSSHServer(t)
	defer server.close()

	// Use a very short TTL for testing
	ttl := 100 * time.Millisecond
	pool := &Pool{TTL: ttl, Logger: tslog.Slogger(t)}
	defer pool.Close()

	t.Log("early")
	config, signer := newTestClientConfig(t)

	// Make first dial
	conn1, err := pool.DialContext(t.Context(), "tcp", "127.0.0.1:80", server.host(), config.User, server.port(), signer, config)
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
	conn2, err := pool.DialContext(t.Context(), "tcp", "127.0.0.1:80", server.host(), config.User, server.port(), signer, config)
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
	// })
}

func TestPoolConcurrentAccess(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		server := newTestSSHServer(t)
		defer server.close()

		pool := &Pool{TTL: 10 * time.Minute, Logger: tslog.Slogger(t)}
		defer pool.Close()

		config, signer := newTestClientConfig(t)

		// Launch multiple goroutines trying to dial concurrently
		const numGoroutines = 10
		conns := make([]net.Conn, numGoroutines)
		errs := make([]error, numGoroutines)
		done := make(chan struct{})

		for i := 0; i < numGoroutines; i++ {
			go func(idx int) {
				conns[idx], errs[idx] = pool.DialContext(t.Context(), "tcp", "127.0.0.1:80", server.host(), config.User, server.port(), signer, config)
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
	})
}

func TestDialWithRetriesContextCancel(t *testing.T) {
	pool := &Pool{TTL: time.Minute, Logger: tslog.Slogger(t)}

	config, signer := newTestClientConfig(t)
	config.Timeout = 10 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	start := time.Now()

	time.AfterFunc(40*time.Millisecond, cancel)

	retries := []time.Duration{200 * time.Millisecond}
	conn, err := pool.DialWithRetries(ctx, "tcp", "127.0.0.1:80", "127.0.0.1", config.User, 65000, signer, config, retries)
	if conn != nil {
		t.Fatal("expected nil connection on cancellation")
	}
	if err == nil {
		t.Fatal("expected at least one error")
	}
	if err := ctx.Err(); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected final error %v, got %v", err, context.Canceled)
	}

	elapsed := time.Since(start)
	if elapsed >= retries[0] {
		t.Fatalf("DialWithRetries respected cancellation too late; elapsed=%v, retry delay=%v", elapsed, retries[0])
	}
}

func TestPoolClose(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		server := newTestSSHServer(t)
		defer server.close()

		pool := &Pool{TTL: 10 * time.Minute, Logger: tslog.Slogger(t)}

		config, signer := newTestClientConfig(t)

		// Make a dial
		conn, err := pool.DialContext(t.Context(), "tcp", "127.0.0.1:80", server.host(), config.User, server.port(), signer, config)
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
	})
}

func TestPoolRecoversFromClosedClient(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		server := newTestSSHServer(t)
		defer server.close()

		pool := &Pool{TTL: time.Minute, Logger: tslog.Slogger(t)}
		defer pool.Close()

		config, signer := newTestClientConfig(t)

		conn, err := pool.DialContext(t.Context(), "tcp", "127.0.0.1:80", server.host(), config.User, server.port(), signer, config)
		if err != nil {
			t.Fatalf("failed initial dial: %v", err)
		}
		conn.Close()

		pool.mu.Lock()
		if len(pool.conns) != 1 {
			pool.mu.Unlock()
			t.Fatalf("expected 1 pooled connection, got %d", len(pool.conns))
		}
		var original *pooledConn
		for _, candidate := range pool.conns {
			original = candidate
		}
		pool.mu.Unlock()

		if original == nil {
			t.Fatal("expected pooled connection to exist")
		}

		if err := original.client.Close(); err != nil {
			t.Fatalf("failed to close underlying client: %v", err)
		}

		_, err = pool.DialContext(t.Context(), "tcp", "127.0.0.1:80", server.host(), config.User, server.port(), signer, config)
		if err == nil {
			t.Fatal("expected error after closing underlying client")
		}

		conn2, err := pool.DialContext(t.Context(), "tcp", "127.0.0.1:80", server.host(), config.User, server.port(), signer, config)
		if err != nil {
			t.Fatalf("failed dial after retry: %v", err)
		}
		conn2.Close()

		pool.mu.Lock()
		if len(pool.conns) != 1 {
			pool.mu.Unlock()
			t.Fatalf("expected 1 pooled connection after recovery, got %d", len(pool.conns))
		}
		var replacement *pooledConn
		for _, candidate := range pool.conns {
			replacement = candidate
		}
		pool.mu.Unlock()

		if replacement == nil {
			t.Fatal("expected replacement pooled connection")
		}
		if replacement == original {
			t.Fatal("expected pool to replace closed SSH client")
		}
	})
}

func TestTrackedConnDoubleClose(t *testing.T) {
	server := newTestSSHServer(t)
	defer server.close()

	pool := &Pool{TTL: 20 * time.Millisecond, Logger: tslog.Slogger(t)}
	defer pool.Close()

	config, signer := newTestClientConfig(t)

	conn, err := pool.DialContext(t.Context(), "tcp", "127.0.0.1:80", server.host(), config.User, server.port(), signer, config)
	if err != nil {
		t.Fatalf("failed initial dial: %v", err)
	}

	if err := conn.Close(); err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
		t.Fatalf("first close returned unexpected error: %v", err)
	}
	if err := conn.Close(); err != nil && !errors.Is(err, net.ErrClosed) && !errors.Is(err, io.EOF) {
		t.Fatalf("second close returned unexpected error: %v", err)
	}

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		pool.mu.Lock()
		remaining := len(pool.conns)
		pool.mu.Unlock()
		if remaining == 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	pool.mu.Lock()
	remaining := len(pool.conns)
	pool.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("expected pooled connection to be removed after double close, still have %d", remaining)
	}

	conn2, err := pool.DialContext(t.Context(), "tcp", "127.0.0.1:80", server.host(), config.User, server.port(), signer, config)
	if err != nil {
		t.Fatalf("failed dial after double close: %v", err)
	}
	conn2.Close()
}

func TestPoolSoak(t *testing.T) {
	server := newTestSSHServer(t)
	defer server.close()

	pool := &Pool{TTL: 40 * time.Millisecond, Logger: tslog.Slogger(t)}
	defer pool.Close()

	config, signer := newTestClientConfig(t)

	ttl := pool.ttl()

	const (
		workers    = 4
		iterations = 80
	)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	errCh := make(chan error, 1)

	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		nextID int
		open   = make(map[int]net.Conn)
	)

	wg.Add(workers)
	for w := 0; w < workers; w++ {
		seed := int64(w + 1)
		go func(seed int64) {
			defer wg.Done()

			r := mathrand.New(mathrand.NewSource(seed))
			for i := 0; i < iterations; i++ {
				if ctx.Err() != nil {
					return
				}

				switch r.Intn(3) {
				case 0:
					conn, err := pool.DialContext(t.Context(), "tcp", "127.0.0.1:80", server.host(), config.User, server.port(), signer, config)
					if err != nil {
						select {
						case errCh <- fmt.Errorf("dial failed: %w", err):
						default:
						}
						cancel()
						return
					}

					mu.Lock()
					id := nextID
					nextID++
					open[id] = conn
					mu.Unlock()

					if r.Float64() < 0.3 {
						mu.Lock()
						delete(open, id)
						mu.Unlock()
						conn.Close()
					}
				case 1:
					var selected net.Conn

					mu.Lock()
					if len(open) > 0 {
						idx := r.Intn(len(open))
						j := 0
						for id, conn := range open {
							if j == idx {
								selected = conn
								delete(open, id)
								break
							}
							j++
						}
					}
					mu.Unlock()

					if selected != nil {
						selected.Close()
					} else {
						time.Sleep(time.Duration(r.Intn(5)+1) * time.Millisecond)
					}
				case 2:
					time.Sleep(time.Duration(r.Intn(5)+1) * time.Millisecond)
				}

				if r.Float64() < 0.1 {
					time.Sleep(ttl + 5*time.Millisecond)
				}
			}
		}(seed)
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case err := <-errCh:
		t.Fatalf("ssh pool soak error: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("ssh pool soak timed out")
	}

	cancel()

	mu.Lock()
	for _, conn := range open {
		conn.Close()
	}
	mu.Unlock()

	select {
	case err := <-errCh:
		t.Fatalf("ssh pool soak error: %v", err)
	default:
	}
}

// blockingProxy is a TCP proxy that can be blocked to simulate a hung connection.
// When blocked, it stops forwarding packets but doesn't close connections,
// simulating what happens when a VM reboots.
type blockingProxy struct {
	listener   net.Listener
	targetAddr string
	addr       string

	mu          sync.Mutex
	blocked     bool
	clientConns []net.Conn
	targetConns []net.Conn
}

func newBlockingProxy(t *testing.T, targetAddr string) *blockingProxy {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to create blocking proxy listener: %v", err)
	}

	p := &blockingProxy{
		listener:   listener,
		targetAddr: targetAddr,
		addr:       listener.Addr().String(),
	}

	go p.serve()
	return p
}

func (p *blockingProxy) serve() {
	for {
		clientConn, err := p.listener.Accept()
		if err != nil {
			return
		}

		p.mu.Lock()
		p.clientConns = append(p.clientConns, clientConn)
		p.mu.Unlock()

		go p.handleConn(clientConn)
	}
}

func (p *blockingProxy) handleConn(clientConn net.Conn) {
	targetConn, err := net.Dial("tcp", p.targetAddr)
	if err != nil {
		clientConn.Close()
		return
	}

	p.mu.Lock()
	p.targetConns = append(p.targetConns, targetConn)
	p.mu.Unlock()

	// Forward in both directions
	done := make(chan struct{}, 2)

	forward := func(dst, src net.Conn) {
		defer func() { done <- struct{}{} }()
		buf := make([]byte, 32*1024)
		for {
			p.mu.Lock()
			blocked := p.blocked
			p.mu.Unlock()

			if blocked {
				// When blocked, just sleep and don't forward anything.
				// This simulates a hung connection.
				time.Sleep(10 * time.Millisecond)
				continue
			}

			src.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
			n, err := src.Read(buf)
			if err != nil {
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					continue
				}
				return
			}
			if n > 0 {
				_, err = dst.Write(buf[:n])
				if err != nil {
					return
				}
			}
		}
	}

	go forward(targetConn, clientConn)
	go forward(clientConn, targetConn)

	<-done
	clientConn.Close()
	targetConn.Close()
	<-done
}

func (p *blockingProxy) block() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.blocked = true
	// Close connections to the target to ensure any pending SSH channel opens
	// will not complete. This simulates a VM reboot where the backend is gone
	// but the client connection is still "open" (TCP half-open state).
	for _, conn := range p.targetConns {
		conn.Close()
	}
	p.targetConns = nil
}

func (p *blockingProxy) host() string {
	host, _, _ := net.SplitHostPort(p.addr)
	return host
}

func (p *blockingProxy) port() int {
	_, portStr, _ := net.SplitHostPort(p.addr)
	port, _ := strconv.Atoi(portStr)
	return port
}

func (p *blockingProxy) close() {
	p.listener.Close()
	p.mu.Lock()
	for _, conn := range p.clientConns {
		conn.Close()
	}
	for _, conn := range p.targetConns {
		conn.Close()
	}
	p.mu.Unlock()
}

// TestPoolStaleConnectionTimeout tests that the pool properly handles
// connections that become unresponsive (timeout) rather than cleanly closed.
// This simulates what happens when a VM reboots - the TCP connection hangs
// rather than returning a clean error.
//
// BEFORE THE FIX: This test demonstrates the bug where timeout errors
// are not recognized as SSH connection errors, so the stale connection
// stays in the pool and subsequent requests also fail.
func TestPoolStaleConnectionTimeout(t *testing.T) {
	// Create real SSH server
	server := newTestSSHServer(t)
	defer server.close()

	// Create a blocking proxy in front of it
	proxy := newBlockingProxy(t, server.addr)
	defer proxy.close()

	pool := &Pool{TTL: time.Minute, Logger: tslog.Slogger(t)}
	defer pool.Close()

	config, signer := newTestClientConfig(t)

	// Establish initial connection through the proxy
	conn, err := pool.DialContext(t.Context(), "tcp", "127.0.0.1:80", proxy.host(), config.User, proxy.port(), signer, config)
	if err != nil {
		t.Fatalf("failed initial dial: %v", err)
	}
	conn.Close()

	// Verify we have a pooled connection
	original := getOnlyPooledConn(t, pool)

	// Now block the proxy to simulate VM reboot (connection hangs, no clean close)
	proxy.block()

	// Try to dial through the stale connection - should fail
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()

	_, err = pool.DialContext(ctx, "tcp", "127.0.0.1:80", proxy.host(), config.User, proxy.port(), signer, config)
	if err == nil {
		t.Fatal("expected dial to fail on blocked proxy")
	}
	t.Logf("first dial error (expected): %v", err)

	// Check if the stale connection was removed from the pool
	pool.mu.Lock()
	connCount := len(pool.conns)
	var current *pooledConn
	for _, pc := range pool.conns {
		current = pc
	}
	pool.mu.Unlock()

	// THE FIX: After an error, the stale connection should be removed from the pool.
	// Before the fix, connCount would be 1 and current == original (stale conn still there).
	// After the fix, connCount should be 0 (stale connection removed).
	if connCount != 0 {
		t.Errorf("expected stale connection to be removed from pool, but pool has %d connections", connCount)
		if current == original {
			t.Error("the pooled connection is still the original stale one")
		}
	}
}

// TestPoolDropConnectionsTo tests that DropConnectionsTo removes connections
// to a specific host/port.
func TestPoolDropConnectionsTo(t *testing.T) {
	server := newTestSSHServer(t)
	defer server.close()

	pool := &Pool{TTL: time.Minute, Logger: tslog.Slogger(t)}
	defer pool.Close()

	config, signer := newTestClientConfig(t)

	// Establish a connection
	conn, err := pool.DialContext(t.Context(), "tcp", "127.0.0.1:80", server.host(), config.User, server.port(), signer, config)
	if err != nil {
		t.Fatalf("failed initial dial: %v", err)
	}
	conn.Close()

	// Verify we have a pooled connection
	pool.mu.Lock()
	if len(pool.conns) != 1 {
		pool.mu.Unlock()
		t.Fatalf("expected 1 pooled connection, got %d", len(pool.conns))
	}
	pool.mu.Unlock()

	// Drop connections to this host/port
	pool.DropConnectionsTo(server.host(), server.port())

	// Verify the connection was removed
	pool.mu.Lock()
	connCount := len(pool.conns)
	pool.mu.Unlock()

	if connCount != 0 {
		t.Errorf("expected 0 connections after DropConnectionsTo, got %d", connCount)
	}

	// Verify we can establish a new connection
	conn2, err := pool.DialContext(t.Context(), "tcp", "127.0.0.1:80", server.host(), config.User, server.port(), signer, config)
	if err != nil {
		t.Fatalf("failed to dial after drop: %v", err)
	}
	conn2.Close()
}

// TestPoolHandshakeTimeout verifies that the 3s connection deadline applies to
// the SSH handshake, not just TCP connect. Before the fix in commit 83ce803,
// ssh.ClientConfig.Timeout only covered TCP connect, so a server that accepted
// TCP but stalled during handshake would block for ~40s (TCP timeout) instead of 3s.
func TestPoolHandshakeTimeout(t *testing.T) {
	t.Skip("super slow, run manually as needed")

	// Create a server that accepts TCP connections but never sends the SSH version banner.
	// This simulates a server that's in a bad state - TCP works but SSH handshake stalls.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	defer listener.Close()

	// Accept connections but never respond (stall the handshake)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			// Hold the connection open but never send anything.
			// This stalls the SSH handshake at version exchange.
			go func(c net.Conn) {
				// Just block until the connection is closed by the client
				buf := make([]byte, 1)
				c.Read(buf) // blocks until closed
			}(conn)
		}
	}()

	host, portStr, _ := net.SplitHostPort(listener.Addr().String())
	port, _ := strconv.Atoi(portStr)

	pool := &Pool{TTL: time.Minute, Logger: tslog.Slogger(t)}
	defer pool.Close()

	config, signer := newTestClientConfig(t)

	start := time.Now()
	_, err = pool.DialContext(t.Context(), "tcp", "127.0.0.1:80", host, config.User, port, signer, config)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected dial to fail on stalling server")
	}

	// The key assertion: with the fix, this should fail in ~3s (the deadline).
	// Before the fix, it would take ~40s (TCP timeout waiting for handshake).
	if elapsed > 5*time.Second {
		t.Fatalf("dial took %v, expected ~3s (handshake timeout not working)", elapsed)
	}
	if elapsed < 2*time.Second {
		t.Fatalf("dial took %v, expected ~3s (too fast, something else failed)", elapsed)
	}

	t.Logf("dial failed in %v as expected (error: %v)", elapsed, err)
}

// TestIsSSHConnErrorRecognizesTimeouts tests that isSSHConnError correctly
// identifies timeout and cancellation errors as connection errors.
func TestIsSSHConnErrorRecognizesTimeouts(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"DeadlineExceeded", context.DeadlineExceeded, true},
		{"Canceled", context.Canceled, true},
		{"EOF", io.EOF, true},
		{"ErrClosed", net.ErrClosed, true},
		{"wrapped DeadlineExceeded", fmt.Errorf("dial failed: %w", context.DeadlineExceeded), true},
		{"wrapped Canceled", fmt.Errorf("dial failed: %w", context.Canceled), true},
		{"random error", errors.New("some random error"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isSSHConnError(tt.err)
			if got != tt.want {
				t.Errorf("isSSHConnError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
