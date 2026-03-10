package exepipe

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"

	"exe.dev/exepipe/client"
	"exe.dev/tslog"
)

func TestListen(t *testing.T) {
	pi, addr := testPipeInstance(t)

	var wg sync.WaitGroup
	wg.Go(func() {
		if err := pi.Start(); err != nil {
			t.Error(err)
		}
	})
	defer wg.Wait()
	defer pi.Stop()

	// The normal use of exepipe listening is to handle
	// connections to an exelet. Each VM on an exelet has
	// an associated port, listening for SSH connections.
	// When a connection arrives on that port,
	// we open a connection to the VM on an internal network.
	// Then we copy data between the connections.
	// This essentially exposes the VM's ssh port
	// to the external network.
	//
	//   - exelet machine listens for TCP connection on port
	//     - externalListener
	//   - VM listens for TCP connection on internal port
	//     - vmListener
	//   - client opens TCP connection to externalListener
	//     - externalClient, externalServer
	//   - listener opens connection to vmListener
	//     - vmClient, vmServer
	//
	// Data is copied between externalServer and vmClient.
	//
	// For thist test we recreate that network setup with
	// localhost sockets.

	externalListener, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatal(err)
	}
	defer externalListener.Close()

	vmListener, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatal(err)
	}
	defer vmListener.Close()

	cli, err := client.NewClient(t.Context(), addr.String(), tslog.Slogger(t))
	if err != nil {
		t.Fatal(err)
	}

	tcpAddr := vmListener.Addr().(*net.TCPAddr)
	if err := cli.Listen(t.Context(), "key", externalListener, tcpAddr.IP.String(), tcpAddr.Port, "test"); err != nil {
		t.Fatal(err)
	}

	// Prepare to receive connections on vmListener.
	ch := make(chan net.Conn, 1)
	go func() {
		vmServer, err := vmListener.Accept()
		if err != nil {
			t.Error(err)
			ch <- nil
			return
		}
		ch <- vmServer
	}()

	// Open a connection to the external listener.
	externalClient, err := net.Dial(externalListener.Addr().Network(), externalListener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer externalClient.Close()

	// That should have opened a connection to vmListener.
	vmServer := <-ch
	if vmServer == nil {
		return // goroutine reported error already
	}
	defer vmServer.Close()

	// Now anything we write to externalClient should be sent to
	// vmServer, and vice-versa.

	const count = 1024

	fromBuf1 := make([]byte, count)
	rand.Read(fromBuf1) // never fails

	fromBuf2 := make([]byte, count)
	rand.Read(fromBuf2)

	n, err := externalClient.Write(fromBuf1)
	if n != count || err != nil {
		t.Fatalf("bad Write: count %d err %v", n, err)
	}

	n, err = vmServer.Write(fromBuf2)
	if n != count || err != nil {
		t.Fatalf("bad Write: count %d err %v", n, err)
	}

	toBuf1 := make([]byte, count)
	n, err = io.ReadFull(externalClient, toBuf1)
	if n != count || err != nil {
		t.Fatalf("bad Read: count %d err %v", n, err)
	}

	toBuf2 := make([]byte, count)
	n, err = io.ReadFull(vmServer, toBuf2)
	if n != count || err != nil {
		t.Fatalf("bad Read: count %d err %v", n, err)
	}

	if !bytes.Equal(fromBuf1, toBuf2) {
		t.Fatalf("bad copy: got %q want %q", toBuf2, fromBuf1)
	}
	if !bytes.Equal(fromBuf2, toBuf1) {
		t.Fatalf("bad copy: got %q want %q", toBuf1, fromBuf2)
	}

	// Shut down the listener.
	if err := cli.Unlisten(t.Context(), "key"); err != nil {
		t.Fatal(err)
	}
	if _, err := net.Dial(externalListener.Addr().Network(), externalListener.Addr().String()); err == nil {
		t.Errorf("connection to closed listener at %s succeeded", externalListener.Addr())
	}

	pi.Stop()
}

func TestListeners(t *testing.T) {
	pi, addr := testPipeInstance(t)

	var wg sync.WaitGroup
	wg.Go(func() {
		if err := pi.Start(); err != nil {
			t.Error(err)
		}
	})
	defer wg.Wait()
	defer pi.Stop()

	cli, err := client.NewClient(t.Context(), addr.String(), tslog.Slogger(t))
	if err != nil {
		t.Fatal(err)
	}

	// Test fetching listeners around the packet limit.
	sofar := 0
	for _, count := range []int{199, 200, 201, 399, 400, 401} {
		i := sofar
		for range count - sofar {
			listener, err := net.Listen("tcp", "localhost:0")
			if err != nil {
				t.Fatal(err)
			}

			if err := cli.Listen(t.Context(), fmt.Sprintf("key%d", i), listener, fmt.Sprintf("host%d", i), i+1, "test"); err != nil {
				t.Fatal(err)
			}
			i++
		}
		sofar = count

		found := 0
		for ln, err := range cli.Listeners(t.Context()) {
			if err != nil {
				t.Fatal(err)
			}
			keyIdx, keyOK := strings.CutPrefix(ln.Key, "key")
			hostIdx, hostOK := strings.CutPrefix(ln.Host, "host")
			if !keyOK || !hostOK || keyIdx != hostIdx || ln.Type != "test" {
				t.Errorf("bad listener %#v", ln)
				continue
			}
			idx, err := strconv.Atoi(keyIdx)
			if err != nil {
				t.Errorf("bad listener index %#v", ln)
				continue
			}
			if idx != ln.Port-1 {
				t.Errorf("bad listener port got %d want %d", ln.Port-1, idx)
				continue
			}

			found++
		}

		if found != count {
			t.Errorf("got %d listeners, want %d", found, count)
		}
	}
}
