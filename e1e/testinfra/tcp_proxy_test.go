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
	wg.Go(func() {
		p.Serve(t.Context())
	})

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
	readWG.Go(func() {
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
	})

	p.SetDestPort(ln.Addr().(*net.TCPAddr).Port)

	readWG.Wait()

	p.Close()

	wg.Wait()
}

// TestTCPProxyRetarget verifies that calling SetDestPort a second time
// redirects new connections to the new destination.
func TestTCPProxyRetarget(t *testing.T) {
	// Backend A
	lnA, err := net.ListenTCP("tcp", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer lnA.Close()

	// Backend B
	lnB, err := net.ListenTCP("tcp", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer lnB.Close()

	p, err := NewTCPProxy("retarget-test")
	if err != nil {
		t.Fatal(err)
	}
	p.SetDestPort(lnA.Addr().(*net.TCPAddr).Port)

	var serveWG sync.WaitGroup
	serveWG.Go(func() {
		p.Serve(t.Context())
	})

	// Verify traffic reaches A.
	const msgA = "hello A\n"
	c1, err := net.DialTCP("tcp", nil, p.Address())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c1.Write([]byte(msgA)); err != nil {
		t.Fatal(err)
	}
	c1.Close()

	lnA.SetDeadline(time.Now().Add(5 * time.Second))
	rc, err := lnA.AcceptTCP()
	if err != nil {
		t.Fatal(err)
	}
	got, err := bufio.NewReader(rc).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if got != msgA {
		t.Fatalf("backend A: got %q want %q", got, msgA)
	}
	rc.Close()

	// Retarget to B.
	p.SetDestPort(lnB.Addr().(*net.TCPAddr).Port)

	const msgB = "hello B\n"
	c2, err := net.DialTCP("tcp", nil, p.Address())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c2.Write([]byte(msgB)); err != nil {
		t.Fatal(err)
	}
	c2.Close()

	lnB.SetDeadline(time.Now().Add(5 * time.Second))
	rc2, err := lnB.AcceptTCP()
	if err != nil {
		t.Fatal(err)
	}
	got2, err := bufio.NewReader(rc2).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if got2 != msgB {
		t.Fatalf("backend B: got %q want %q", got2, msgB)
	}
	rc2.Close()

	p.Close()
	serveWG.Wait()
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
	serveWG.Go(func() {
		p.Serve(t.Context())
	})

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
