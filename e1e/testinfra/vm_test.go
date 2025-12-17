package testinfra

import (
	"fmt"
	"math/rand/v2"
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

func TestVM(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in short mode")
	}

	t.Parallel()

	testRunID := fmt.Sprintf("%04x", rand.Uint32()&0xFFFF)

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

	t.Logf("exelet VM running at %s", ctrHost)

	// Make sure we can connect to the VM.
	// TODO(ian): We should have a function to run an ssh command.
	sshHost := strings.TrimPrefix(ctrHost, "ssh://")
	cmd := exec.CommandContext(t.Context(), "ssh",
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=no",
		"-o", "LogLevel=ERROR",
		"-o", "UserKnownHostsFile=/dev/null",
		sshHost,
		"uptime")

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Errorf("failed to ssh to %s: %v\n%s", sshHost, err, out)
	}

	// The cleanup filed by StartExeletVM should take care of
	// shutting down the VM if necessary.
}
