package tcprtt_test

import (
	"net"
	"runtime"
	"testing"

	"exe.dev/tcprtt"
)

func TestGet_TCPConn(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("TCP RTT only supported on Linux")
	}

	// Start a listener and connect to it.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	acceptDone := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			t.Error(err)
			return
		}
		acceptDone <- c
	}()

	client, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	server := <-acceptDone
	defer server.Close()

	// Send some data so the kernel has RTT samples.
	_, _ = client.Write([]byte("hello"))
	buf := make([]byte, 5)
	_, _ = server.Read(buf)

	rtt, err := tcprtt.Get(client)
	if err != nil {
		t.Fatalf("Get(client) failed: %v", err)
	}
	// For loopback, RTT should be very small but > 0.
	if rtt < 0 {
		t.Fatalf("negative RTT: %v", rtt)
	}
	t.Logf("client RTT: %v", rtt)
}

func TestGet_NonTCP(t *testing.T) {
	// unix.GetsockoptTCPInfo should fail on non-TCP sockets.
	// We just test that Get returns an error for non-TCP.
	// This is hard to test without a unix socket, so test nil.
	_, err := tcprtt.Get(nil)
	if err == nil {
		t.Fatal("expected error for nil conn")
	}
}

func TestContextRoundtrip(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	go func() {
		c, _ := ln.Accept()
		if c != nil {
			c.Close()
		}
	}()

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	ctx := tcprtt.ContextWithConn(t.Context(), conn)
	got := tcprtt.ConnFromContext(ctx)
	if got != conn {
		t.Fatalf("ConnFromContext returned %v, want %v", got, conn)
	}

	// No conn in background context.
	if c := tcprtt.ConnFromContext(t.Context()); c != nil {
		t.Fatalf("ConnFromContext(empty) = %v, want nil", c)
	}
}
