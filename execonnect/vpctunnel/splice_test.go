package vpctunnel

import (
	"io"
	"net"
	"testing"
)

func TestSpliceCopiesBothDirections(t *testing.T) {
	leftPeer, leftTunnel := tcpPair(t)
	rightTunnel, rightPeer := tcpPair(t)

	result := make(chan [2]int64, 1)
	go func() {
		leftToRight, rightToLeft := Splice(leftTunnel, rightTunnel)
		result <- [2]int64{leftToRight, rightToLeft}
	}()

	leftPayload := []byte("left to right")
	rightPayload := []byte("right to left")
	writeAndClose(t, leftPeer, leftPayload)
	writeAndClose(t, rightPeer, rightPayload)

	if got, err := io.ReadAll(rightPeer); err != nil {
		t.Fatal(err)
	} else if string(got) != string(leftPayload) {
		t.Fatalf("right payload = %q, want %q", got, leftPayload)
	}
	if got, err := io.ReadAll(leftPeer); err != nil {
		t.Fatal(err)
	} else if string(got) != string(rightPayload) {
		t.Fatalf("left payload = %q, want %q", got, rightPayload)
	}

	counts := <-result
	if counts != [2]int64{int64(len(leftPayload)), int64(len(rightPayload))} {
		t.Fatalf("copy counts = %v", counts)
	}
}

func tcpPair(t *testing.T) (*net.TCPConn, *net.TCPConn) {
	t.Helper()
	listener, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	dialed := make(chan *net.TCPConn, 1)
	dialError := make(chan error, 1)
	go func() {
		connection, err := net.DialTCP("tcp", nil, listener.Addr().(*net.TCPAddr))
		if err != nil {
			dialError <- err
			return
		}
		dialed <- connection
	}()

	accepted, err := listener.AcceptTCP()
	if err != nil {
		t.Fatal(err)
	}
	var peer *net.TCPConn
	select {
	case peer = <-dialed:
	case err := <-dialError:
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = peer.Close() })
	t.Cleanup(func() { _ = accepted.Close() })
	return peer, accepted
}

func writeAndClose(t *testing.T, connection *net.TCPConn, payload []byte) {
	t.Helper()
	if _, err := connection.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := connection.CloseWrite(); err != nil {
		t.Fatal(err)
	}
}
