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
	"strings"
	"sync"
	"syscall"
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

		for i := range 3 {
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

	for i := range 2 {
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

		for i := range numGoroutines {
			go func(idx int) {
				conns[idx], errs[idx] = pool.DialContext(t.Context(), "tcp", "127.0.0.1:80", server.host(), config.User, server.port(), signer, config)
				done <- struct{}{}
			}(i)
		}

		// Wait for all goroutines to complete
		for range numGoroutines {
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

		// The onConnClosed watcher may proactively remove the dead
		// connection before our next dial (depending on goroutine
		// scheduling). If so, the first dial succeeds immediately.
		// If not, it fails and the retry succeeds. Either way, the
		// pool must recover.
		conn2, err := pool.DialContext(t.Context(), "tcp", "127.0.0.1:80", server.host(), config.User, server.port(), signer, config)
		if err != nil {
			conn2, err = pool.DialContext(t.Context(), "tcp", "127.0.0.1:80", server.host(), config.User, server.port(), signer, config)
		}
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
	for w := range workers {
		seed := int64(w + 1)
		go func(seed int64) {
			defer wg.Done()

			r := mathrand.New(mathrand.NewSource(seed))
			for range iterations {
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

// TestDialWithRetriesRetriesDialThroughClient tests that DialWithRetries
// retries failures that occur during dialThroughClient (port forwarding),
// not just during connection establishment.
func TestDialWithRetriesRetriesDialThroughClient(t *testing.T) {
	// Create a server that rejects the first N port-forward attempts
	// but accepts subsequent ones.
	server := newTestSSHServerWithFailingPortForward(t, 2) // fail first 2 attempts
	defer server.close()

	pool := &Pool{TTL: time.Minute, Logger: tslog.Slogger(t)}
	defer pool.Close()

	config, signer := newTestClientConfig(t)

	// Use retries that should be enough to get past the 2 failures
	retries := []time.Duration{10 * time.Millisecond, 10 * time.Millisecond, 10 * time.Millisecond}

	// DialWithRetries should retry the entire operation (connect + dialThroughClient),
	// so it should eventually succeed after the first 2 port-forward failures.
	// Note: err may be non-nil even on success (contains errors from prior attempts).
	conn, err := pool.DialWithRetries(t.Context(), "tcp", "127.0.0.1:80", server.host(), config.User, server.port(), signer, config, retries)
	if conn == nil {
		t.Fatalf("DialWithRetries failed to get connection: %v", err)
	}
	if err != nil {
		t.Logf("DialWithRetries succeeded with prior errors (expected): %v", err)
	}
	conn.Close()

	// Verify we made at least 3 port-forward attempts (2 failures + 1 success)
	if server.portForwardAttempts() < 3 {
		t.Errorf("expected at least 3 port forward attempts, got %d", server.portForwardAttempts())
	}
}

// testSSHServerWithFailingPortForward is a test SSH server that fails
// the first N port-forward (direct-tcpip channel) requests.
type testSSHServerWithFailingPortForward struct {
	*testSSHServer
	failCount int // how many port-forwards to fail

	mu       sync.Mutex
	attempts int // count of port-forward attempts
}

func newTestSSHServerWithFailingPortForward(t *testing.T, failCount int) *testSSHServerWithFailingPortForward {
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
		NoClientAuth: true,
	}
	config.AddHostKey(signer)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}

	s := &testSSHServerWithFailingPortForward{
		testSSHServer: &testSSHServer{
			listener: listener,
			config:   config,
			addr:     listener.Addr().String(),
		},
		failCount: failCount,
	}

	go s.serveWithFailingPortForward()

	return s
}

func (s *testSSHServerWithFailingPortForward) serveWithFailingPortForward() {
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

			go ssh.DiscardRequests(reqs)

			for newChannel := range chans {
				go func(newChannel ssh.NewChannel) {
					if newChannel.ChannelType() == "direct-tcpip" {
						s.mu.Lock()
						s.attempts++
						shouldFail := s.attempts <= s.failCount
						s.mu.Unlock()

						if shouldFail {
							newChannel.Reject(ssh.ConnectionFailed, "simulated port-forward failure")
							return
						}

						channel, _, err := newChannel.Accept()
						if err != nil {
							return
						}
						channel.Close()
					} else {
						newChannel.Reject(ssh.UnknownChannelType, "not supported")
					}
				}(newChannel)
			}
		}(conn)
	}
}

func (s *testSSHServerWithFailingPortForward) portForwardAttempts() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.attempts
}

// TestDialWithRetriesStaleConnectionRecovery tests that DialWithRetries
// can recover from a stale pooled connection within a single call.
//
// Scenario:
// 1. Establish a connection, close it (remains in pool)
// 2. Kill the underlying SSH client (simulating VM reboot)
// 3. Call DialWithRetries - it should recover via retries
func TestDialWithRetriesStaleConnectionRecovery(t *testing.T) {
	server := newTestSSHServer(t)
	defer server.close()

	pool := &Pool{TTL: time.Minute, Logger: tslog.Slogger(t)}
	defer pool.Close()

	config, signer := newTestClientConfig(t)

	// Step 1: Establish a connection
	conn, err := pool.DialContext(t.Context(), "tcp", "127.0.0.1:80", server.host(), config.User, server.port(), signer, config)
	if err != nil {
		t.Fatalf("initial dial failed: %v", err)
	}
	conn.Close()

	// Step 2: Get the pooled connection and close its underlying SSH client
	// (simulating the server going away / VM reboot)
	pc := getOnlyPooledConn(t, pool)
	if err := pc.client.Close(); err != nil {
		t.Fatalf("failed to close underlying client: %v", err)
	}
	t.Log("Closed underlying SSH client to simulate stale connection")

	// Step 3: Try to dial with retries - should recover within single call
	// Note: err may be non-nil even on success (contains errors from prior attempts).
	retries := []time.Duration{10 * time.Millisecond, 50 * time.Millisecond, 100 * time.Millisecond}
	conn2, err := pool.DialWithRetries(t.Context(), "tcp", "127.0.0.1:80", server.host(), config.User, server.port(), signer, config, retries)
	if conn2 == nil {
		t.Fatalf("DialWithRetries failed to recover from stale connection: %v", err)
	}
	if err != nil {
		t.Logf("DialWithRetries recovered with prior errors (expected): %v", err)
	}
	t.Log("DialWithRetries recovered from stale connection within single call")
	conn2.Close()
}

// TestRunCommandBasic tests the RunCommand functionality
func TestRunCommandBasic(t *testing.T) {
	server := newTestSSHServerWithExec(t)
	defer server.close()

	pool := &Pool{TTL: time.Minute, Logger: tslog.Slogger(t)}
	defer pool.Close()

	config, signer := newTestClientConfig(t)
	connRetries := []time.Duration{10 * time.Millisecond, 50 * time.Millisecond}

	output, err := pool.RunCommand(t.Context(), server.host(), config.User, server.port(), signer, config, "echo hello", nil, connRetries)
	if err != nil {
		t.Fatalf("RunCommand failed: %v", err)
	}

	if string(output) != "hello\n" {
		t.Errorf("unexpected output: %q, expected %q", string(output), "hello\n")
	}
}

// TestRunCommandWithStdin tests RunCommand with stdin
func TestRunCommandWithStdin(t *testing.T) {
	server := newTestSSHServerWithExec(t)
	defer server.close()

	pool := &Pool{TTL: time.Minute, Logger: tslog.Slogger(t)}
	defer pool.Close()

	config, signer := newTestClientConfig(t)
	connRetries := []time.Duration{10 * time.Millisecond, 50 * time.Millisecond}

	stdin := strings.NewReader("world")
	output, err := pool.RunCommand(t.Context(), server.host(), config.User, server.port(), signer, config, "cat", stdin, connRetries)
	if err != nil {
		t.Fatalf("RunCommand with stdin failed: %v", err)
	}

	if string(output) != "world" {
		t.Errorf("unexpected output: %q, expected %q", string(output), "world")
	}
}

// TestRunCommandStaleConnectionRecovery tests that RunCommand retries connection
// establishment when encountering a stale pooled connection.
// Note: only the connection is retried, not the command itself (commands are not idempotent).
func TestRunCommandStaleConnectionRecovery(t *testing.T) {
	server := newTestSSHServerWithExec(t)
	defer server.close()

	pool := &Pool{TTL: time.Minute, Logger: tslog.Slogger(t)}
	defer pool.Close()

	config, signer := newTestClientConfig(t)
	connRetries := []time.Duration{10 * time.Millisecond, 50 * time.Millisecond, 100 * time.Millisecond}

	// Establish a connection
	output, err := pool.RunCommand(t.Context(), server.host(), config.User, server.port(), signer, config, "echo hello", nil, connRetries)
	if err != nil {
		t.Fatalf("initial RunCommand failed: %v", err)
	}
	t.Logf("Initial command output: %s", output)

	// Close the underlying SSH client to simulate stale connection
	pc := getOnlyPooledConn(t, pool)
	if err := pc.client.Close(); err != nil {
		t.Fatalf("failed to close underlying client: %v", err)
	}
	t.Log("Closed underlying SSH client to simulate stale connection")

	// RunCommand should fail on this stale connection (connection retries don't help
	// because the stale connection is already in the pool and looks "alive").
	// The stale connection will be removed, so a subsequent call should work.
	output, err = pool.RunCommand(t.Context(), server.host(), config.User, server.port(), signer, config, "echo recovered", nil, connRetries)
	if err == nil {
		// If it somehow succeeded, that's fine too
		t.Log("RunCommand succeeded on first try after stale connection")
		t.Logf("Output: %s", output)
		return
	}

	t.Logf("RunCommand failed on stale connection (expected): %v", err)

	// Verify the stale connection was removed
	pool.mu.Lock()
	connCount := len(pool.conns)
	pool.mu.Unlock()
	if connCount != 0 {
		t.Errorf("expected stale connection to be removed, pool has %d connections", connCount)
	}

	// A subsequent call SHOULD work because stale connection was removed
	output, err = pool.RunCommand(t.Context(), server.host(), config.User, server.port(), signer, config, "echo recovered", nil, connRetries)
	if err != nil {
		t.Fatalf("second RunCommand failed: %v", err)
	}
	t.Log("Second RunCommand succeeded (fresh connection)")
	t.Logf("Output: %s", output)
}

// testSSHServerWithExec is a test SSH server that can execute commands
type testSSHServerWithExec struct {
	listener net.Listener
	config   *ssh.ServerConfig
	addr     string
}

func newTestSSHServerWithExec(t *testing.T) *testSSHServerWithExec {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate server key: %v", err)
	}

	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatalf("failed to create signer: %v", err)
	}

	config := &ssh.ServerConfig{
		NoClientAuth: true,
	}
	config.AddHostKey(signer)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}

	s := &testSSHServerWithExec{
		listener: listener,
		config:   config,
		addr:     listener.Addr().String(),
	}

	go s.serve(t)

	return s
}

func (s *testSSHServerWithExec) serve(t *testing.T) {
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

			go ssh.DiscardRequests(reqs)

			for newChannel := range chans {
				go func(newChannel ssh.NewChannel) {
					if newChannel.ChannelType() == "session" {
						channel, requests, err := newChannel.Accept()
						if err != nil {
							return
						}

						go func() {
							for req := range requests {
								switch req.Type {
								case "exec":
									// Parse the command
									if len(req.Payload) < 4 {
										req.Reply(false, nil)
										continue
									}
									cmdLen := int(req.Payload[0])<<24 | int(req.Payload[1])<<16 | int(req.Payload[2])<<8 | int(req.Payload[3])
									if len(req.Payload) < 4+cmdLen {
										req.Reply(false, nil)
										continue
									}
									cmd := string(req.Payload[4 : 4+cmdLen])
									req.Reply(true, nil)

									// Execute the command (simple simulation)
									var output string
									switch {
									case strings.HasPrefix(cmd, "echo "):
										output = strings.TrimPrefix(cmd, "echo ") + "\n"
									case cmd == "cat":
										// Read from channel (stdin)
										buf := make([]byte, 1024)
										n, _ := channel.Read(buf)
										output = string(buf[:n])
									default:
										output = "unknown command\n"
									}

									channel.Write([]byte(output))

									// Send exit status
									channel.SendRequest("exit-status", false, []byte{0, 0, 0, 0})
									channel.Close()
									return
								default:
									req.Reply(false, nil)
								}
							}
						}()
					} else if newChannel.ChannelType() == "direct-tcpip" {
						channel, _, err := newChannel.Accept()
						if err != nil {
							return
						}
						channel.Close()
					} else {
						newChannel.Reject(ssh.UnknownChannelType, "not supported")
					}
				}(newChannel)
			}
		}(conn)
	}
}

func (s *testSSHServerWithExec) close() {
	s.listener.Close()
}

func (s *testSSHServerWithExec) host() string {
	host, _, _ := net.SplitHostPort(s.addr)
	return host
}

func (s *testSSHServerWithExec) port() int {
	_, portStr, _ := net.SplitHostPort(s.addr)
	port, _ := strconv.Atoi(portStr)
	return port
}

func TestClassifyError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		// success
		{"nil", nil, "success"},

		// error_stale — must precede timeout check because
		// ErrStaleConnection wraps DeadlineExceeded via %w.
		{"stale", ErrStaleConnection, "error_stale"},
		{"wrapped stale", fmt.Errorf("dial: %w", ErrStaleConnection), "error_stale"},
		{"stale beats deadline", fmt.Errorf("channel: %w",
			fmt.Errorf("stale: %w", ErrStaleConnection)), "error_stale"},

		// error_cancelled
		{"canceled", context.Canceled, "error_cancelled"},
		{"wrapped canceled", fmt.Errorf("op: %w", context.Canceled), "error_cancelled"},

		// error_timeout
		{"deadline exceeded", context.DeadlineExceeded, "error_timeout"},
		{"wrapped deadline", fmt.Errorf("dial: %w", context.DeadlineExceeded), "error_timeout"},

		// error_backend_refused
		{"connection refused", fmt.Errorf("open failed: Connection refused"), "error_backend_refused"},

		// error_transport
		{"eof", io.EOF, "error_transport"},
		{"net closed", net.ErrClosed, "error_transport"},
		{"econnreset", syscall.ECONNRESET, "error_transport"},
		{"epipe", syscall.EPIPE, "error_transport"},
		{"etimedout", syscall.ETIMEDOUT, "error_transport"},
		{"wrapped eof", fmt.Errorf("read: %w", io.EOF), "error_transport"},

		// error_command
		{"exit error", &ssh.ExitError{Waitmsg: ssh.Waitmsg{}}, "error_command"},
		{"wrapped exit error", fmt.Errorf("cmd: %w", &ssh.ExitError{Waitmsg: ssh.Waitmsg{}}), "error_command"},

		// error_other
		{"unknown", errors.New("something unexpected"), "error_other"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyError(tt.err)
			if got != tt.want {
				t.Errorf("classifyError(%v) = %q, want %q", tt.err, got, tt.want)
			}
		})
	}
}

func TestOnConnClosedCalledWhenSSHDies(t *testing.T) {
	server := newTestSSHServer(t)
	defer server.close()

	callbackDone := make(chan struct{})
	var gotHost string
	var gotUser string
	var gotPort int
	var gotPublicKey string

	pool := &Pool{
		TTL:    time.Minute,
		Logger: tslog.Slogger(t),
		OnConnClosed: func(host string, user string, port int, publicKey string) {
			gotHost = host
			gotUser = user
			gotPort = port
			gotPublicKey = publicKey
			close(callbackDone)
		},
	}
	defer pool.Close()

	config, signer := newTestClientConfig(t)

	conn, err := pool.DialContext(t.Context(), "tcp", "127.0.0.1:80", server.host(), config.User, server.port(), signer, config)
	if err != nil {
		t.Fatalf("failed initial dial: %v", err)
	}
	conn.Close()

	// Get the pooled SSH client and close it, simulating SSH death.
	pool.mu.Lock()
	var original *pooledConn
	for _, candidate := range pool.conns {
		original = candidate
	}
	pool.mu.Unlock()
	if original == nil {
		t.Fatal("no pooled connection found")
	}
	original.client.Close()

	// The OnConnClosed callback should fire.
	select {
	case <-callbackDone:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for OnConnClosed callback")
	}

	if gotHost != server.host() {
		t.Errorf("got host %q, want %q", gotHost, server.host())
	}
	if gotUser != config.User {
		t.Errorf("got user %q, want %q", gotUser, config.User)
	}
	if gotPort != server.port() {
		t.Errorf("got port %d, want %d", gotPort, server.port())
	}
	wantPublicKey := string(signer.PublicKey().Marshal())
	if gotPublicKey != wantPublicKey {
		t.Errorf("got publicKey %q, want %q", gotPublicKey, wantPublicKey)
	}

	// The dead connection should have been removed from the pool.
	pool.mu.Lock()
	n := len(pool.conns)
	pool.mu.Unlock()
	if n != 0 {
		t.Errorf("expected 0 pooled connections after death, got %d", n)
	}
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
		{"ECONNRESET", syscall.ECONNRESET, true},
		{"EPIPE", syscall.EPIPE, true},
		{"wrapped ECONNRESET", &net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET}, true},
		{"wrapped EPIPE", &net.OpError{Op: "write", Net: "tcp", Err: syscall.EPIPE}, true},
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
