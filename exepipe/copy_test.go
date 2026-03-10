package exepipe

import (
	"bytes"
	"crypto/rand"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"testing"
	"time"

	"exe.dev/exepipe/client"
	"exe.dev/tslog"
)

func TestCopy(t *testing.T) {
	pi, addr := testPipeInstance(t)

	var wg sync.WaitGroup
	wg.Go(func() {
		if err := pi.Start(); err != nil {
			t.Error(err)
		}
	})
	defer wg.Wait()
	defer pi.Stop()

	// The normal use of exepipe copying is for a client
	// connecting to a VM via HTTP or SSH.
	//
	//   - client opens TCP connection to HTTPS or SSH port
	//     - externalClient
	//   - exeprox/exed accepts and authenticates connection
	//     - externalServerListener, externalServerConn
	//   - exeprox/exed opens an SSH connection to the VM
	//     - internalClient
	//   - the VM accepts the connection
	//     - internalServerListener, internalServerConn
	//   - exepipe then copies data between
	//     externalServerConn and internalClient
	//
	// For this test we recreate that network setup with localhost sockets.

	externalServerListener, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatal(err)
	}
	defer externalServerListener.Close()

	externalClient, externalServerConn := testConn(t, externalServerListener)
	if externalClient == nil || externalServerConn == nil {
		return // function reported error
	}
	defer externalClient.Close()
	defer externalServerConn.Close()

	internalServerListener, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatal(err)
	}
	defer internalServerListener.Close()

	internalClient, internalServerConn := testConn(t, internalServerListener)
	if internalClient == nil || internalServerConn == nil {
		return // function reported error
	}
	defer internalClient.Close()
	defer internalServerConn.Close()

	// The sockets are set up. Now ask exepipe to copy.

	cli, err := client.NewClient(t.Context(), addr.String(), tslog.Slogger(t))
	if err != nil {
		t.Fatal(err)
	}

	if err := cli.Copy(t.Context(), externalServerConn, internalClient, "test"); err != nil {
		t.Fatal(err)
	}

	// Send data both ways across the connections.

	const count = 1024

	fromBuf1 := make([]byte, count)
	rand.Read(fromBuf1) // never fails

	fromBuf2 := make([]byte, count)
	rand.Read(fromBuf2)

	n, err := externalClient.Write(fromBuf1)
	if n != count || err != nil {
		t.Fatalf("bad Write: count %d err %v", n, err)
	}

	n, err = internalServerConn.Write(fromBuf2)
	if n != count || err != nil {
		t.Fatalf("bad Write: count %d err %v", n, err)
	}

	toBuf1 := make([]byte, count)
	n, err = io.ReadFull(externalClient, toBuf1)
	if n != count || err != nil {
		t.Fatalf("bad Read: count %d err %v", n, err)
	}

	toBuf2 := make([]byte, count)
	n, err = io.ReadFull(internalServerConn, toBuf2)
	if n != count || err != nil {
		t.Fatalf("bad Read: count %d err %v", n, err)
	}

	if !bytes.Equal(fromBuf1, toBuf2) {
		t.Fatalf("bad copy: got %q want %q", toBuf2, fromBuf1)
	}
	if !bytes.Equal(fromBuf2, toBuf1) {
		t.Fatalf("bad copy: got %q want %q", toBuf1, fromBuf2)
	}

	externalClient.Close()
	internalServerConn.Close()

	checkMetrics(t, pi)

	pi.Stop()
}

// testConn takes a listener and opens a connection to that listener,
// returning clientConn (returned by net.Dial) and serverConn
// (returned by ln.Accept). If an error occurs it is reported
// and testConn returns nil, nil.
func testConn(t *testing.T, ln net.Listener) (clientConn, serverConn net.Conn) {
	ch := make(chan net.Conn, 1)
	go func() {
		serverConn, err := ln.Accept()
		if err != nil {
			t.Error(err)
			ch <- nil
			return
		}
		ch <- serverConn
	}()

	clientConn, err := net.Dial(ln.Addr().Network(), ln.Addr().String())
	if err != nil {
		t.Error(err)
		return nil, nil
	}

	serverConn = <-ch
	if serverConn == nil {
		// goroutine reported error
		clientConn.Close()
		return nil, nil
	}

	return clientConn, serverConn
}

// checkMetrics verifies that the metrics are recorded for TestCopy.
func checkMetrics(t *testing.T, pi *PipeInstance) {
	// Wait for connections to stabilize.
	// TODO: This is a hack.
	for pi.piping.connsCount() > 0 {
		t.Logf("checkmetrics: connsCount == %d", pi.piping.connsCount())
		time.Sleep(time.Millisecond)
	}

	resp, err := http.Get("http://" + pi.httpServer.ln.Addr().String() + "/metrics")
	if err != nil {
		t.Errorf("failed to fetch metrics: %v", err)
		return
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Errorf("failed to read metrics: %v", err)
		return
	}
	resp.Body.Close()

	showMetrics := false
	want := `copy_sessions_total{type="test"} 1`
	if !bytes.Contains(data, []byte(want)) {
		t.Errorf("metrics did not contain %q", want)
		showMetrics = true
	}
	want = `copy_bytes_total{type="test"} 2048`
	if !bytes.Contains(data, []byte(want)) {
		t.Errorf("metrics did not contain %q", want)
		showMetrics = true
	}
	if showMetrics {
		t.Logf("Metrics:\n%s", data)
	}
}

// Test that copy uses splice system call on Linux.
func TestSplice(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("test only runs on Linux")
	}

	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("/usr/bin/strace", "-f", exe, "-test.run=TestCopy")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("strace failed: %v\n%s", err, out)
	}

	count := bytes.Count(out, []byte("] splice"))
	if count == 0 {
		t.Error("no calls to splice")
		t.Logf("strace output:\n%s", out)
	} else {
		t.Logf("%d calls to splice", count)
	}
}
