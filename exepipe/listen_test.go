package exepipe

import (
	"bytes"
	"crypto/rand"
	"io"
	"net"
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

	if err := cli.Listen(t.Context(), externalListener, vmListener.Addr().String(), "test"); err != nil {
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

	pi.Stop()
}
