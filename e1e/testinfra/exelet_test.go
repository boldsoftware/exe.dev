package testinfra

import (
	"fmt"
	"math/rand/v2"
	"os"
	"runtime"
	"testing"

	api "exe.dev/pkg/api/exe/compute/v1"
)

func TestExelet(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in short mode")
	}
	if os.Getenv("CI") != "" {
		t.Skip("skipping on CI")
	}

	t.Parallel()

	testRunID := fmt.Sprintf("%04x", rand.Uint32()&0xFFFF)

	exeletBinary, err := BuildExeletBinary(testRunID)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(exeletBinary)

	ctrHost, err := StartExeletVM(testRunID)
	if err != nil {
		if err == ErrNoVM {
			// We can see ErrNoVM on Darwin,
			// but we shouldn't see it on Linux.
			if runtime.GOOS == "linux" {
				t.Fatal(err)
			}
			t.Skipf("skipping test: %v", err)
		}

		t.Fatal(err)
	}

	// Start a TCP proxy that in a real case would connect
	// to the exed, although in this case it will just
	// accept connections.
	fakeExedProxy, err := NewTCPProxy("test-exelet")
	if err != nil {
		t.Fatal(err)
	}
	go fakeExedProxy.Serve(t.Context())
	defer fakeExedProxy.Close()

	ei, err := StartExelet(t.Context(), exeletBinary, ctrHost, fakeExedProxy.Port(), testRunID, nil, false)
	if err != nil {
		t.Fatal(err)
	}

	client := ei.Client()
	resp, err := client.GetSystemInfo(t.Context(), &api.GetSystemInfoRequest{})
	if err != nil {
		t.Fatal(err)
	}

	t.Logf("exelet version %s arch %s", resp.Version, resp.Arch)

	dir := ei.Stop(t.Context())
	if dir != "" {
		os.RemoveAll(dir)
	}
}
