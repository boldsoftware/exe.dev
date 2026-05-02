//go:build linux

package exepipe

import (
	"bytes"
	"os/exec"
	"strconv"
	"testing"

	"github.com/mdlayher/vsock"
)

// Test listen connecting to a vsock port.
func TestVSockListen(t *testing.T) {
	out, err := exec.Command("lsmod").CombinedOutput()
	if err != nil {
		t.Skipf("skipping because running lsmod failed: %v\n%s", err, out)
	}
	if !bytes.Contains(out, []byte("vhost_vsock")) || !bytes.Contains(out, []byte("vsock_loopback")) {
		t.Skipf("skipping because lsmod does not include vhost_vsock or vsock_loopback\n%s", out)
	}

	vmListener, err := vsock.ListenContextID(vsock.Host, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	vsockAddr := vmListener.Addr().(*vsock.Addr)
	testListen(t, vmListener, strconv.Itoa(vsock.Host), int(vsockAddr.Port))
}
