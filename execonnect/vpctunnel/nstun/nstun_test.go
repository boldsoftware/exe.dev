package nstun

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"testing"
	"time"
)

func TestNetTCPRoundTrip(t *testing.T) {
	tunDevice, network, err := Create([]netip.Addr{netip.MustParseAddr("fd65:7865::1")}, 1280)
	if err != nil {
		t.Fatal(err)
	}
	defer tunDevice.Close()

	listener, err := network.ListenTCP(4700)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	serverErr := make(chan error, 1)
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			serverErr <- err
			return
		}
		defer connection.Close()
		_, err = io.Copy(connection, connection)
		serverErr <- err
	}()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	connection, err := network.DialContextTCP(ctx, netip.MustParseAddrPort("[fd65:7865::1]:4700"))
	if err != nil {
		t.Fatal(err)
	}

	payload := []byte("hello")
	if _, err := connection.Write(payload); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(connection, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != string(payload) {
		t.Fatalf("echo = %q, want %q", got, payload)
	}
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-serverErr; err != nil && !errors.Is(err, net.ErrClosed) {
		t.Fatal(err)
	}
}

func TestNetCloseIsIdempotent(t *testing.T) {
	tunDevice, _, err := Create(nil, 1280)
	if err != nil {
		t.Fatal(err)
	}
	if err := tunDevice.Close(); err != nil {
		t.Fatal(err)
	}
	if err := tunDevice.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCreateRejectsInvalidMTU(t *testing.T) {
	if _, _, err := Create(nil, 0); err == nil {
		t.Fatal("expected error")
	}
}
