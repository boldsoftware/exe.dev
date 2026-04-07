package exepipe

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
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
	defer cli.Close()

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
	testCopy(t, externalClient, vmServer)

	checkListenerMetrics(t, pi, 1)

	// Shut down the listener.
	if err := cli.Unlisten(t.Context(), "key"); err != nil {
		t.Fatal(err)
	}
	if _, err := net.Dial(externalListener.Addr().Network(), externalListener.Addr().String()); err == nil {
		t.Errorf("connection to closed listener at %s succeeded", externalListener.Addr())
	}

	checkListenerMetrics(t, pi, 0)

	pi.Stop()
}

// checkListenerMetrics verifies that the metrics are reported for TestListen.
func checkListenerMetrics(t *testing.T, pi *PipeInstance, active int) {
	resp, err := http.Get("http://" + pi.httpServer.ln.Addr().String() + "/metrics")
	if err != nil {
		t.Errorf("failed to fetch metrics: %v", err)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Errorf("failed to read metrics: %v", err)
		return
	}
	resp.Body.Close()

	showMetrics := false
	want := `listeners_total{type="test"} 1`
	if !bytes.Contains(data, []byte(want)) {
		t.Errorf("metrics did not contain %q", want)
		showMetrics = true
	}
	want = fmt.Sprintf(`listeners_active{type="test"} %d`, active)
	if !bytes.Contains(data, []byte(want)) {
		t.Errorf("metrics did not contain %q", want)
		showMetrics = true
	}
	if showMetrics {
		t.Logf("Metrics:\n%s", data)
	}
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
	defer cli.Close()

	// Test fetching listeners around the packet limit.
	sofar := 0
	for _, count := range []int{199, 200, 201, 399, 400, 401} {
		i := sofar
		for range count - sofar {
			listener, err := net.Listen("tcp", "localhost:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()

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
