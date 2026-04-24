//go:build linux

package exepipe

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"exe.dev/exepipe/client"
	"exe.dev/stage"
	"exe.dev/tslog"

	"github.com/prometheus/client_golang/prometheus"
	vns "github.com/vishvananda/netns"
)

const testNsName = "exepipe-test-ns"

func skipUnlessRoot(t *testing.T) {
	t.Helper()
	if os.Getuid() != 0 {
		t.Skip("requires root")
	}
}

// createTestNetns creates a network namespace with loopback up and
// the given IP assigned to the loopback interface.
func createTestNetns(t *testing.T, nsName, ip string, port int) {
	if runtime.GOOS != "linux" {
		t.Skipf("skipping test that uses network namespaces on %s", runtime.GOOS)
	}

	t.Helper()

	// Clean up any stale namespace from a previous run.
	exec.Command("ip", "netns", "delete", nsName).Run()

	// Create namespace.
	out, err := exec.Command("ip", "netns", "add", nsName).CombinedOutput()
	if err != nil {
		t.Fatalf("ip netns add %s: %v\n%s", nsName, err, out)
	}
	t.Cleanup(func() {
		exec.Command("ip", "netns", "delete", nsName).Run()
	})

	// Bring up loopback in the namespace.
	out, err = exec.Command("ip", "netns", "exec", nsName, "ip", "link", "set", "lo", "up").CombinedOutput()
	if err != nil {
		t.Fatalf("bring up lo: %v\n%s", err, out)
	}

	// Add the test IP to the loopback interface in the namespace.
	out, err = exec.Command("ip", "netns", "exec", nsName, "ip", "addr", "add", ip+"/16", "dev", "lo").CombinedOutput()
	if err != nil {
		t.Fatalf("add addr to lo: %v\n%s", err, out)
	}
}

// startListenerInNetns starts a TCP listener inside the given network
// namespace on ip:port. The listener runs socat to echo back data.
// Returns a cleanup function.
func startListenerInNetns(t *testing.T, nsName, ip string, port int) {
	t.Helper()

	addr := net.JoinHostPort(ip, strconv.Itoa(port))
	cmd := exec.Command("ip", "netns", "exec", nsName,
		"socat", "TCP-LISTEN:"+strconv.Itoa(port)+",bind="+ip+",reuseaddr,fork", "EXEC:cat")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start socat in netns on %s: %v", addr, err)
	}
	t.Cleanup(func() {
		cmd.Process.Kill()
		cmd.Wait()
	})

	// Wait for the listener to be ready.
	for i := range 50 {
		out, err := exec.Command("ip", "netns", "exec", nsName,
			"ss", "-tln").CombinedOutput()
		if err == nil && strings.Contains(string(out), strconv.Itoa(port)) {
			return
		}
		time.Sleep(time.Duration(i+1) * 10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for socat listener in netns on %s", addr)
}

// TestNetnsDialFunc_EntersNamespace verifies that NetnsDialFunc actually
// dials from within the specified network namespace. We create a netns
// with a private IP and a TCP echo server, then verify we can reach it
// only through the namespace-aware dial.
func TestNetnsDialFunc_EntersNamespace(t *testing.T) {
	skipUnlessRoot(t)

	const (
		testIP   = "10.42.0.42"
		testPort = 7771
	)

	createTestNetns(t, testNsName, testIP, testPort)
	startListenerInNetns(t, testNsName, testIP, testPort)

	ctx := t.Context()
	lg := tslog.Slogger(t)

	// Dial through the namespace — should succeed.
	conn, err := dialNetns(ctx, lg, testNsName, testIP, testPort, 2*time.Second)
	if err != nil {
		t.Fatalf("dial in netns failed: %v", err)
	}
	defer conn.Close()

	// Send data and verify echo.
	msg := []byte("hello-from-netns-test")
	if _, err := conn.Write(msg); err != nil {
		t.Fatalf("write: %v", err)
	}

	buf := make([]byte, len(msg))
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Read(buf); err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(buf) != string(msg) {
		t.Fatalf("echo mismatch: got %q, want %q", buf, msg)
	}
}

// TestNetnsDialFunc_RootNamespaceUnreachable verifies that the IP inside
// the network namespace is NOT reachable from the root namespace via
// normal dialing (no netns). This confirms that the namespace-aware
// dial is doing real work.
func TestNetnsDialFunc_RootNamespaceUnreachable(t *testing.T) {
	skipUnlessRoot(t)

	const (
		testIP   = "10.42.0.42"
		testPort = 7772
	)

	createTestNetns(t, testNsName, testIP, testPort)
	startListenerInNetns(t, testNsName, testIP, testPort)

	ctx := t.Context()
	lg := tslog.Slogger(t)

	// Dial WITHOUT namespace — should fail (10.42.0.42 has no route in root ns).
	conn, err := dialNetns(ctx, lg, "", testIP, testPort, 500*time.Millisecond)
	if err == nil {
		conn.Close()
		t.Fatal("expected dial to 10.42.0.42 in root namespace to fail, but it succeeded")
	}
	t.Logf("correctly failed to dial in root ns: %v", err)
}

// TestNetnsDialFunc_NonexistentNamespace verifies that dialing with a
// nonexistent namespace name returns a clear error.
func TestNetnsDialFunc_NonexistentNamespace(t *testing.T) {
	skipUnlessRoot(t)

	ctx := t.Context()
	lg := tslog.Slogger(t)

	_, err := dialNetns(ctx, lg, "nonexistent-ns-12345", "10.42.0.42", 22, 500*time.Millisecond)
	if err == nil {
		t.Fatal("expected error for nonexistent namespace")
	}
	if !strings.Contains(err.Error(), "nonexistent-ns-12345") {
		t.Fatalf("error should mention namespace name, got: %v", err)
	}
}

// TestNetnsDialFunc_EmptyNsName verifies that when nsName is empty,
// NetnsDialFunc does a normal dial without touching namespaces.
func TestNetnsDialFunc_EmptyNsName(t *testing.T) {
	// This test doesn't require root or Linux-specific features
	// beyond what the build tag gives us.

	// Start a regular TCP listener in the current namespace.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	addr := ln.Addr().(*net.TCPAddr)

	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()

	conn, err := dialNetns(t.Context(), tslog.Slogger(t), "", "127.0.0.1", addr.Port, time.Second)
	if err != nil {
		t.Fatalf("dial with empty nsName: %v", err)
	}
	conn.Close()
}

// TestNetnsDialFunc_RestoredAfterDial verifies that the goroutine's
// network namespace is restored to the original after the dial completes.
func TestNetnsDialFunc_RestoredAfterDial(t *testing.T) {
	skipUnlessRoot(t)

	const (
		testIP   = "10.42.0.42"
		testPort = 7773
	)

	createTestNetns(t, testNsName, testIP, testPort)
	startListenerInNetns(t, testNsName, testIP, testPort)

	// Get the current namespace before dialing.
	origNS, err := vns.Get()
	if err != nil {
		t.Fatal(err)
	}
	defer origNS.Close()

	// Dial in the namespace.
	conn, err := dialNetns(t.Context(), tslog.Slogger(t), testNsName, testIP, testPort, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	conn.Close()

	// Verify we're back in the original namespace.
	// A simple check: dialing a localhost listener should work.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen after dial — namespace may not have been restored: %v", err)
	}
	ln.Close()
}

// TestNetnsDialFunc_ConcurrentDials verifies that concurrent dials into
// different or same namespaces don't interfere with each other.
func TestNetnsDialFunc_ConcurrentDials(t *testing.T) {
	skipUnlessRoot(t)

	const (
		testIP   = "10.42.0.42"
		testPort = 7774
	)

	createTestNetns(t, testNsName, testIP, testPort)
	startListenerInNetns(t, testNsName, testIP, testPort)

	ctx := t.Context()
	lg := tslog.Slogger(t)

	var wg sync.WaitGroup
	errs := make(chan error, 20)

	for i := range 20 {
		wg.Go(func() {
			conn, err := dialNetns(ctx, lg, testNsName, testIP, testPort, 5*time.Second)
			if err != nil {
				errs <- fmt.Errorf("goroutine %d: %w", i, err)
				return
			}
			// Quick echo test.
			msg := []byte(fmt.Sprintf("ping-%d", i))
			conn.Write(msg)
			buf := make([]byte, len(msg))
			conn.SetReadDeadline(time.Now().Add(2 * time.Second))
			_, err = conn.Read(buf)
			conn.Close()
			if err != nil {
				errs <- fmt.Errorf("goroutine %d read: %w", i, err)
			}
		})
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		t.Error(err)
	}
}

// TestListenWithNetns verifies the full exepipe stack: client sends a
// listen command with a netns name, and when a connection arrives on
// the external listener, exepipe dials the target through the namespace.
func TestListenWithNetns(t *testing.T) {
	skipUnlessRoot(t)

	const (
		testIP   = "10.42.0.42"
		testPort = 7775
	)

	createTestNetns(t, testNsName, testIP, testPort)
	startListenerInNetns(t, testNsName, testIP, testPort)

	// Create an exepipe with netns-aware dialing.
	addr := testUnixAddr(t)
	pc := &PipeConfig{
		Env:             stage.Test(),
		Logger:          tslog.Slogger(t),
		UnixAddr:        addr,
		HTTPPort:        "0",
		MetricsRegistry: prometheus.NewRegistry(),
	}

	pi, err := NewPipe(pc)
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	wg.Go(func() {
		if err := pi.Start(); err != nil {
			t.Error(err)
		}
	})
	defer wg.Wait()
	defer pi.Stop()

	// Create an external listener (in root ns).
	externalListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer externalListener.Close()

	// Tell exepipe to listen and forward to testIP:testPort in the namespace.
	cli, err := client.NewClient(t.Context(), addr.String(), tslog.Slogger(t))
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()

	if err := cli.Listen(t.Context(), "ns-test", externalListener, testNsName, testIP, testPort, "ssh"); err != nil {
		t.Fatal(err)
	}

	// Connect to the external listener.
	conn, err := net.Dial("tcp", externalListener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	// Write data — it should go through exepipe → netns dial → socat echo.
	msg := []byte("exepipe-netns-integration")
	if _, err := conn.Write(msg); err != nil {
		t.Fatalf("write: %v", err)
	}

	buf := make([]byte, len(msg))
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Read(buf); err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(buf) != string(msg) {
		t.Fatalf("echo mismatch: got %q, want %q", buf, msg)
	}
}

// TestListenWithNetnsPreservedInListeners verifies that the netns name
// is preserved when querying exepipe's active listeners.
func TestListenWithNetnsPreservedInListeners(t *testing.T) {
	skipUnlessRoot(t)

	addr := testUnixAddr(t)
	pc := &PipeConfig{
		Env:             stage.Test(),
		Logger:          tslog.Slogger(t),
		UnixAddr:        addr,
		HTTPPort:        "0",
		MetricsRegistry: prometheus.NewRegistry(),
	}

	pi, err := NewPipe(pc)
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	wg.Go(func() {
		if err := pi.Start(); err != nil {
			t.Error(err)
		}
	})
	defer wg.Wait()
	defer pi.Stop()

	externalListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer externalListener.Close()

	cli, err := client.NewClient(t.Context(), addr.String(), tslog.Slogger(t))
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()

	if err := cli.Listen(t.Context(), "ns-key", externalListener, "exe-vm000001", "10.42.0.42", 22, "ssh"); err != nil {
		t.Fatal(err)
	}

	// Query listeners and verify the netns is preserved.
	found := false
	for ln, err := range cli.Listeners(t.Context()) {
		if err != nil {
			t.Fatal(err)
		}
		if ln.Key == "ns-key" {
			found = true
			if ln.Netns != "exe-vm000001" {
				t.Errorf("listener netns: got %q, want %q", ln.Netns, "exe-vm000001")
			}
			if ln.Host != "10.42.0.42" {
				t.Errorf("listener host: got %q, want %q", ln.Host, "10.42.0.42")
			}
			if ln.Port != 22 {
				t.Errorf("listener port: got %d, want 22", ln.Port)
			}
		}
	}
	if !found {
		t.Error("listener 'ns-key' not found in exepipe")
	}
}

// TestNetnsPreservedAcrossTransfer verifies that when a new exepipe takes
// over from an old one, listeners with netns names are transferred correctly.
func TestNetnsPreservedAcrossTransfer(t *testing.T) {
	skipUnlessRoot(t)

	addr := testUnixAddr(t)
	pc1 := &PipeConfig{
		Env:             stage.Test(),
		Logger:          tslog.Slogger(t),
		UnixAddr:        addr,
		HTTPPort:        "0",
		MetricsRegistry: prometheus.NewRegistry(),
	}

	pi1, err := NewPipe(pc1)
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	wg.Go(func() {
		if err := pi1.Start(); err != nil {
			t.Error(err)
		}
	})
	defer wg.Wait()
	defer pi1.Stop()

	// Set up a listener with a netns.
	externalListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer externalListener.Close()

	cli, err := client.NewClient(t.Context(), addr.String(), tslog.Slogger(t))
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()

	if err := cli.Listen(t.Context(), "transfer-key", externalListener, "exe-vm000099", "10.42.0.42", 22, "ssh"); err != nil {
		t.Fatal(err)
	}

	// Start a new exepipe on the same address (triggers transfer).
	pc2 := &PipeConfig{
		Env:             stage.Test(),
		Logger:          tslog.Slogger(t),
		UnixAddr:        addr,
		HTTPPort:        "0",
		MetricsRegistry: prometheus.NewRegistry(),
	}

	pi2, err := NewPipe(pc2)
	if err != nil {
		t.Fatal(err)
	}
	wg.Go(func() {
		if err := pi2.Start(); err != nil {
			t.Error(err)
		}
	})
	defer pi2.Stop()

	// Wait for transfer to complete.
	for i := range 100 {
		if !pi2.transferringNew.Load() {
			break
		}
		time.Sleep(time.Duration(i+1) * time.Millisecond)
	}
	if pi2.transferringNew.Load() {
		t.Fatal("transfer did not complete")
	}

	// Query the new exepipe's listeners.
	cli2, err := client.NewClient(t.Context(), addr.String(), tslog.Slogger(t))
	if err != nil {
		t.Fatal(err)
	}
	defer cli2.Close()

	found := false
	for ln, err := range cli2.Listeners(t.Context()) {
		if err != nil {
			t.Fatal(err)
		}
		if ln.Key == "transfer-key" {
			found = true
			if ln.Netns != "exe-vm000099" {
				t.Errorf("transferred listener netns: got %q, want %q", ln.Netns, "exe-vm000099")
			}
			if ln.Host != "10.42.0.42" {
				t.Errorf("transferred listener host: got %q, want %q", ln.Host, "10.42.0.42")
			}
			if ln.Port != 22 {
				t.Errorf("transferred listener port: got %d, want 22", ln.Port)
			}
		}
	}
	if !found {
		t.Error("listener 'transfer-key' not found after transfer")
	}
}

// testUnixAddr returns a random abstract Unix address for testing.
func testUnixAddr(t *testing.T) *net.UnixAddr {
	t.Helper()
	return &net.UnixAddr{
		Name: fmt.Sprintf("@exepipetest-netns-%d", time.Now().UnixNano()),
		Net:  "unixpacket",
	}
}
