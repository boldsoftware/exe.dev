package exepipe

import (
	"bytes"
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"exe.dev/exepipe/client"
	"exe.dev/stage"
	"exe.dev/tslog"

	"github.com/prometheus/client_golang/prometheus"
)

func TestTransfer(t *testing.T) {
	// Test that a new exepipe picks up cleanly from an existing one.

	// Start an exepipe.

	pi1, addr := testPipeInstance(t)

	var wg sync.WaitGroup
	wg.Go(func() {
		if err := pi1.Start(); err != nil {
			t.Error(err)
		}
	})
	defer wg.Wait()
	defer pi1.Stop()

	// Set up a listener.

	externalListener1, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatal(err)
	}
	defer externalListener1.Close()

	vmListener1, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatal(err)
	}
	defer vmListener1.Close()

	cli, err := client.NewClient(t.Context(), addr.String(), tslog.Slogger(t))
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()

	tcpAddr := vmListener1.Addr().(*net.TCPAddr)
	if err := cli.Listen(t.Context(), "key1", externalListener1, "", tcpAddr.IP.String(), tcpAddr.Port, "test"); err != nil {
		t.Fatal(err)
	}

	listen := func(ln net.Listener, ch chan net.Conn) {
		for {
			c, err := ln.Accept()
			if err != nil {
				if !errors.Is(err, net.ErrClosed) {
					t.Error(err)
				}
				break
			}
			ch <- c
		}
	}

	ch1 := make(chan net.Conn, 1)
	wg.Go(func() { listen(vmListener1, ch1) })

	// Open a connection using the listener we set up.

	externalClient1, err := net.Dial(externalListener1.Addr().Network(), externalListener1.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer externalClient1.Close()

	vmServer1 := <-ch1
	if vmServer1 == nil {
		return // goroutine reported error
	}
	defer vmServer1.Close()

	// Wait for the exepipe copy process to be up and running.
	if n, err := externalClient1.Write(make([]byte, 1)); err != nil {
		t.Fatal(err)
	} else if n != 1 {
		t.Fatal("failed to write expected byte")
	}
	if n, err := vmServer1.Read(make([]byte, 1)); err != nil {
		t.Fatal(err)
	} else if n != 1 {
		t.Fatal("failed to read expected byte")
	}

	// Now we have an existing exepipe with a listener and an
	// ongoing copy. Start a new exepipe on the same address,
	// which should take over from the existing one.

	pc := &PipeConfig{
		Env:             stage.Test(),
		Logger:          tslog.Slogger(t),
		UnixAddr:        addr,
		HTTPPort:        "0",
		MetricsRegistry: prometheus.NewRegistry(),
	}
	pi2, err := NewPipe(pc)
	if err != nil {
		t.Fatal(err)
	}
	wg.Go(func() {
		if err := pi2.Start(); err != nil {
			t.Error(err)
		}
	})
	defer pi2.Stop()

	// Wait until the listener has transferred away from the old exepipe.
	waitForMetrics(t, pi1, `listeners_active{type="test"} 0`)
	waitForMetrics(t, pi2, `listeners_active{type="test"} 1`)

	// We expect one copy connection remaining on the old exepipe.
	waitForMetrics(t, pi1, `copy_sessions_in_flight{type="test"} 1`)

	// Wait until everything is transferred.
	for i := range 100 {
		if !pi2.transferringNew.Load() {
			break
		}
		time.Sleep(time.Duration(i) * time.Millisecond)
	}
	if pi2.transferringNew.Load() {
		t.Fatal("transfer did not complete")
	}

	// Set up another listener using the existing client.

	externalListener2, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatal(err)
	}
	defer externalListener2.Close()

	vmListener2, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatal(err)
	}
	defer vmListener2.Close()

	tcpAddr = vmListener2.Addr().(*net.TCPAddr)
	if err := cli.Listen(t.Context(), "key2", externalListener2, "", tcpAddr.IP.String(), tcpAddr.Port, "test"); err != nil {
		t.Fatal(err)
	}

	ch2 := make(chan net.Conn, 1)
	wg.Go(func() { listen(vmListener2, ch2) })

	externalClient2, err := net.Dial(externalListener2.Addr().Network(), externalListener2.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer externalClient2.Close()

	vmServer2 := <-ch2
	if vmServer2 == nil {
		return // goroutine reported error
	}
	defer vmServer2.Close()

	// Wait for the exepipe copy process to be up and running.
	if n, err := externalClient2.Write(make([]byte, 1)); err != nil {
		t.Fatal(err)
	} else if n != 1 {
		t.Fatal("failed to write expected byte")
	}
	if n, err := vmServer2.Read(make([]byte, 1)); err != nil {
		t.Fatal(err)
	} else if n != 1 {
		t.Fatal("failed to read expected byte")
	}

	// The new connection should be on the new exepipe.
	waitForMetrics(t, pi1, `copy_sessions_in_flight{type="test"} 1`)
	waitForMetrics(t, pi2, `copy_sessions_in_flight{type="test"} 1`)

	// Test that both connections work.
	testCopy(t, externalClient1, vmServer1)
	testCopy(t, externalClient2, vmServer2)

	// Close the last connection on the old exepipe.
	externalClient1.Close()
	vmServer1.Close()

	// That should stop the exepipe.
	var metricsErr error
	for i := range 100 {
		resp, err := http.Get("http://" + pi1.httpServer.ln.Addr().String() + "/metrics")
		if err != nil {
			metricsErr = err
			break
		}
		resp.Body.Close()
		time.Sleep(time.Duration(i) * time.Millisecond)
	}
	if metricsErr != nil {
		t.Logf("old exepipe stopped with %v", metricsErr)
	} else {
		t.Error("old exepipe did not shut down")
	}
}

// waitForMetrics waits for the metrics on pi to include want.
func waitForMetrics(t *testing.T, pi *PipeInstance, want string) {
	t.Helper()

	var metrics []byte
	for i := range 100 {
		resp, err := http.Get("http://" + pi.httpServer.ln.Addr().String() + "/metrics")
		if err != nil {
			t.Fatal(err)
		}

		metrics, err = io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()

		if bytes.Contains(metrics, []byte(want)) {
			return
		}

		time.Sleep(time.Duration(i) * time.Millisecond)
	}

	t.Fatalf("did not find %q in metrics\n%s", want, metrics)
}
