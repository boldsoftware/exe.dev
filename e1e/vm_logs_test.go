package e1e

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"
)

// TestVMLogsForStoppedVM exercises the full user-visible recovery flow for a
// VM that the user can't SSH to anymore:
//
//  1. Create a VM and wait for it to be ready.
//  2. Power it off from inside. The next SSH attempt now finds the VM
//     not-running, the same situation a user hits when their VM has
//     failed to start.
//  3. SSH to the box. The piper plugin denies public-key auth and emits an
//     SSH_MSG_USERAUTH_BANNER that names the VM, points the user at
//     `<ssh-command> vm-logs <name>`, and offers `rm <name>` as the delete
//     escape hatch. Critically, the banner must NOT embed log content: logs
//     are served by the authenticated `vm-logs` REPL command only.
//  4. The user follows the suggestion and runs `vm-logs <name>` through the
//     exe.dev REPL. Ownership is checked inside the handler, and the stopped
//     VM's startup logs are streamed back.
func TestVMLogsForStoppedVM(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 1)
	noGolden(t)

	pty, _, keyFile, _ := registerForExeDev(t)

	// Step 1: create a VM normally.
	bn := newBox(t, pty)
	pty.Disconnect()

	// Step 2: wait for SSH, then power the VM off from inside. We used
	// to `sudo shutdown now`, but on images that don't run systemd
	// (exe-init-only VMs) shutdown can't reach logind over dbus and
	// exits with "Failed to connect to bus" without actually halting.
	// `poweroff --force --force` is documented by systemd to bypass the
	// init system and call reboot(2) directly, so it works regardless
	// of logind state. We fire it one-shot and don't wait for the SSH
	// command to return — it won't, cleanly: the kernel powers off
	// mid-syscall and the TCP connection dies. We then poll until SSH
	// actually stops answering, which is our signal that the hypervisor
	// has noticed the power-off and the piper will see STOPPED state.
	waitForSSH(t, bn, keyFile)

	poweroffCtx, poweroffCancel := context.WithTimeout(Env.context(t), 15*time.Second)
	_ = Env.servers.BoxSSHCommand(poweroffCtx, bn, keyFile, "sudo", "poweroff", "--force", "--force").Run()
	poweroffCancel()

	// Tight poll — piper's STOPPED-state banner is what we actually
	// want to test in step 3, so move on as soon as SSH refuses us.
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		probeCtx, cancel := context.WithTimeout(Env.context(t), 2*time.Second)
		err := Env.servers.BoxSSHCommand(probeCtx, bn, keyFile, "true").Run()
		cancel()
		if err != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Step 3: try to SSH to the now-stopped box. The piper plugin's
	// publickey handler denies the attempt with an AuthDenialError banner
	// that points the user at `vm-logs <name>`. We force publickey-only so
	// OpenSSH doesn't fall through to keyboard-interactive; the banner is
	// emitted inline by the pubkey failure path.
	failPty := makePty(t, "ssh stopped vm")
	sshCmd := exec.CommandContext(Env.context(t), "ssh",
		"-p", strconv.Itoa(Env.servers.SSHPiperd.Port),
		"-o", "IdentityFile="+keyFile,
		"-o", "IdentityAgent=none",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "PreferredAuthentications=publickey",
		"-o", "PubkeyAuthentication=yes",
		"-o", "KbdInteractiveAuthentication=no",
		"-o", "PasswordAuthentication=no",
		"-o", "IdentitiesOnly=yes",
		"-o", "ConnectTimeout=10",
		"-F", "/dev/null",
		bn+"@localhost",
	)
	sshCmd.Env = append(os.Environ(), "SSH_AUTH_SOCK=") // disable SSH agent
	failPty.AttachAndStart(sshCmd)

	// The denial banner format is:
	//   VM "name" is not running.
	//
	//   To see why it failed, run:
	//
	//       <ssh-command> vm-logs <name>
	//
	//   To delete it, run:
	//
	//       <ssh-command> rm <name>
	failPty.Want(fmt.Sprintf("VM %q is not running", bn))
	failPty.Want("vm-logs " + bn)
	failPty.Want("rm " + bn)
	failPty.WantEOF()
	_ = sshCmd.Wait()

	// Step 4: the user follows the suggestion and runs vm-logs through the
	// exe.dev REPL. Use --json so the test isn't fragile to ANSI / formatting
	// (and so RunExeDevSSHCommand's ANSI guard doesn't reject the output).
	// The handler streams logs from exelet and returns them under the
	// "logs" key; we only care that the call succeeded and returned
	// something for the right VM, since the actual log content depends on
	// image state.
	resp := runParseExeDevJSON[struct {
		VMName string   `json:"vm_name"`
		Logs   []string `json:"logs"`
	}](t, keyFile, "vm-logs", bn, "--json")
	if resp.VMName != bn {
		t.Errorf("vm-logs returned wrong vm_name: got %q, want %q", resp.VMName, bn)
	}
	if len(resp.Logs) == 0 {
		t.Errorf("vm-logs returned no log lines for stopped VM %q", bn)
	}

	cleanupBox(t, keyFile, bn)
}
