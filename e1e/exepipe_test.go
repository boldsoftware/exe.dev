package e1e

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"exe.dev/e1e/testinfra"
)

// Test that exepipe -controller behaves as intended with systemd.
func TestExepipeController(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 1)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	exepipeTester := filepath.Join(t.TempDir(), "exepipe_tester")
	cmd := exec.Command("go", "build", "-o", exepipeTester, "exepipe_tester.go")
	cmd.Stdout = t.Output()
	cmd.Stderr = t.Output()
	if err := cmd.Run(); err != nil {
		t.Fatalf("go build exepipe_tester.go failed: %v", err)
	}

	pty, _, keyFile, _ := registerForExeDev(t)
	boxName := newBox(t, pty, testinfra.BoxOpts{NoEmail: true})
	pty.Disconnect()
	defer cleanupBox(t, keyFile, boxName)

	waitForSSH(t, boxName, keyFile)

	run := func(command string, stdin io.Reader) {
		cmd := boxSSHCommand(t, boxName, keyFile, command)
		cmd.Stdin = stdin
		cmd.Stdout = t.Output()
		cmd.Stderr = t.Output()
		if err := cmd.Run(); err != nil {
			t.Fatalf("%s failed: %v", command, err)
		}
	}

	var exepipeBin string
	if Env.servers.Exepipe != nil {
		exepipeBin = Env.servers.Exepipe.BinPath
	} else {
		exepipeBin = filepath.Join(t.TempDir(), "exepipe")
		buildCmd := exec.Command("go", "build", "-race", "-o", exepipeBin, "./cmd/exepipe")
		buildCmd.Dir = ".."
		if out, err := buildCmd.CombinedOutput(); err != nil {
			t.Fatalf("failed to build exepipe: %v\n%s", err, out)
		}
	}

	bin, err := os.Open(exepipeBin)
	if err != nil {
		t.Fatalf("can't open exepipe binary: %v", err)
	}
	run("cat >exepipe", bin)
	bin.Close()

	run("chmod +x exepipe", nil)

	bin, err = os.Open(exepipeTester)
	if err != nil {
		t.Fatalf("can't open exepipe_tester binary: %v", err)
	}
	run("cat >exepipe_tester", bin)
	bin.Close()

	run("chmod +x exepipe_tester", nil)

	run("sudo bash -c 'cat >/etc/systemd/system/exepipe.service'", strings.NewReader(exepipeServiceFile))
	run("sudo bash -c 'cat >/etc/default/exepipe'", strings.NewReader(exepipeEnv))
	run("sudo systemctl start exepipe", nil)

	// The rest of the test is run as a program on the VM.
	run("./exepipe_tester", nil)
}

// exepipeServiceFile is written to /etc/systemd/system/exepipe.service.
var exepipeServiceFile = `
[Unit]
Description=exe.dev exepipe
After=network.target
Wants=network-online.target

[Service]
Type=simple
User=exedev
Group=exedev
WorkingDirectory=/home/exedev
EnvironmentFile=/etc/default/exepipe

ExecStart=/home/exedev/exepipe -controller -addr @exepipe -stage prod -http-port=

Restart=always
RestartSec=5

# Don't kill child processes
KillMode=process

# Use journald for logging (remove file redirection)
StandardOutput=journal
StandardError=journal

# Allow binding to privileged ports
AmbientCapabilities=CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_NET_BIND_SERVICE

[Install]
WantedBy=multi-user.target
`

// exepipeEnv is written to /etc/default/exepipe
var exepipeEnv = `
LOG_FORMAT=json
`
