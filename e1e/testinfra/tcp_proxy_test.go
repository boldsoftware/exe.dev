package testinfra

import (
	"bufio"
	"net"
	"sync"
	"testing"
	"time"
)

func TestTCPProxy(t *testing.T) {
	p, err := NewTCPProxy("test")
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		p.Serve(t.Context())
	}()

	c, err := net.DialTCP("tcp", nil, p.Address())
	if err != nil {
		t.Fatal(err)
	}
	const msg = "hello\n"
	if _, err := c.Write([]byte(msg)); err != nil {
		t.Fatal(err)
	}

	ln, err := net.ListenTCP("tcp", nil)
	if err != nil {
		t.Fatal(err)
	}

	var readWG sync.WaitGroup
	readWG.Add(1)
	go func() {
		defer readWG.Done()

		rc, err := ln.AcceptTCP()
		if err != nil {
			t.Error(err)
			return
		}

		rcb := bufio.NewReader(rc)
		s, err := rcb.ReadString('\n')
		if err != nil {
			t.Error(err)
			return
		}

		if s != msg {
			t.Errorf("read %q want %q", s, msg)
		}
	}()

	p.SetDestPort(ln.Addr().(*net.TCPAddr).Port)

	readWG.Wait()

	p.Close()

	wg.Wait()
}

// TestTCPProxyHalfClose verifies that when the destination side closes
// the connection, the proxy goroutines clean up promptly rather than
// leaking goroutines stuck in io.Copy/splice.
func TestTCPProxyHalfClose(t *testing.T) {
	// Set up a destination server that accepts, reads one buffer, then closes.
	dstLn, err := net.ListenTCP("tcp", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer dstLn.Close()

	go func() {
		for {
			conn, err := dstLn.AcceptTCP()
			if err != nil {
				return
			}
			// Read one line and close immediately.
			buf := make([]byte, 256)
			conn.Read(buf)
			conn.Close()
		}
	}()

	p, err := NewTCPProxy("half-close-test")
	if err != nil {
		t.Fatal(err)
	}
	p.SetDestPort(dstLn.Addr().(*net.TCPAddr).Port)

	var serveWG sync.WaitGroup
	serveWG.Add(1)
	go func() {
		defer serveWG.Done()
		p.Serve(t.Context())
	}()

	// Make multiple connections through the proxy and close the client side.
	// Before the fix, proxy goroutines would leak on each connection because
	// the io.Copy in one direction would block forever in splice after the
	// other direction closed.
	for i := range 20 {
		c, err := net.DialTCP("tcp", nil, p.Address())
		if err != nil {
			t.Fatalf("iter %d: dial failed: %v", i, err)
		}
		c.Write([]byte("hello\n"))
		// Close our side; the destination already closed.
		c.Close()
	}

	// Close the proxy. Before the fix, Close() would hang because proxy
	// goroutines were stuck in wg.Wait() with leaked io.Copy goroutines.
	done := make(chan struct{})
	go func() {
		p.Close()
		serveWG.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Success: proxy cleaned up promptly.
	case <-time.After(5 * time.Second):
		t.Fatal("TCPProxy.Close() did not return within 5 seconds; proxy goroutines are likely leaked")
	}
}
