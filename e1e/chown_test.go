package e1e

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestChownVM tests the `sudo-exe chown` command end-to-end.
//
// It verifies that a VM can be reassigned from one user account to another,
// and that:
//   - the new owner sees the VM in `ls`
//   - the old owner does not
//   - the new owner can SSH into it (piper routing follows ownership)
//   - existing shares (individual, pending) are cleared
//   - the exelet reparents the VM's cgroup into the new account's slice
//   - non-sudo users cannot invoke the command
func TestChownVM(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 1)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	// Three users: the sudo user (operator), the donor (original owner), the recipient.
	// Plus a bystander used to verify share cleanup.
	opPTY, _, opKeyFile, opEmail := registerForExeDevWithEmail(t, "op@test-chown.example")
	donorPTY, _, donorKeyFile, donorEmail := registerForExeDevWithEmail(t, "donor@test-chown.example")
	recipientPTY, _, recipientKeyFile, recipientEmail := registerForExeDevWithEmail(t, "recipient@test-chown.example")
	bystanderPTY, _, _, bystanderEmail := registerForExeDevWithEmail(t, "bystander@test-chown.example")
	opPTY.Disconnect()
	recipientPTY.Disconnect()
	bystanderPTY.Disconnect()

	enableRootSupport(t, opEmail)

	donorID := getUserIDByEmail(t, donorEmail)
	recipientID := getUserIDByEmail(t, recipientEmail)

	// Donor creates a VM.
	box := newBox(t, donorPTY)

	// Donor adds a share with the bystander and a pending share with an unknown email,
	// so we can verify chown clears them all.
	donorPTY.SendLine(fmt.Sprintf("share add %s %s", box, bystanderEmail))
	donorPTY.Want("Invitation sent")
	donorPTY.WantPrompt()
	donorPTY.SendLine(fmt.Sprintf("share add %s pendinguser@test-chown.example", box))
	donorPTY.Want("Invitation sent")
	donorPTY.WantPrompt()
	donorPTY.Disconnect()

	waitForSSH(t, box, donorKeyFile)

	t.Run("Usage", func(t *testing.T) {
		noGolden(t)
		repl := sshToExeDev(t, opKeyFile)
		defer repl.Disconnect()
		repl.SendLine("sudo-exe chown")
		repl.Want("usage")
		repl.WantPrompt()
		repl.SendLine("sudo-exe chown onlyone")
		repl.Want("usage")
		repl.WantPrompt()
	})

	t.Run("NonSudoRejected", func(t *testing.T) {
		noGolden(t)
		// Donor has no root_support; the command is gated by RequiresSudo.
		repl := sshToExeDev(t, donorKeyFile)
		defer repl.Disconnect()
		repl.SendLine("sudo-exe chown " + box + " " + recipientEmail)
		repl.Want("sudoers")
		repl.WantPrompt()
	})

	t.Run("UnknownVM", func(t *testing.T) {
		noGolden(t)
		repl := sshToExeDev(t, opKeyFile)
		defer repl.Disconnect()
		repl.SendLine("sudo-exe chown nonexistent-vm-xyz " + recipientEmail)
		repl.Want("not found")
		repl.WantPrompt()
	})

	t.Run("UnknownUser", func(t *testing.T) {
		noGolden(t)
		repl := sshToExeDev(t, opKeyFile)
		defer repl.Disconnect()
		repl.SendLine("sudo-exe chown " + box + " no-such-user@test-chown.example")
		repl.Want("user not found")
		repl.WantPrompt()
	})

	t.Run("AlreadyOwned", func(t *testing.T) {
		noGolden(t)
		repl := sshToExeDev(t, opKeyFile)
		defer repl.Disconnect()
		repl.SendLine("sudo-exe chown " + box + " " + donorEmail)
		repl.Want("already owned")
		repl.WantPrompt()
	})

	// Subtests that depend on the chown succeeding consult chowned.Load().
	var chowned atomic.Bool
	needsChown := func(t *testing.T) {
		t.Helper()
		if !chowned.Load() {
			t.Skip("prior Chown subtest did not succeed")
		}
	}

	// Happy path.
	t.Run("Chown", func(t *testing.T) {
		noGolden(t)
		out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), opKeyFile, "sudo-exe", "chown", box, recipientEmail, "--json")
		if err != nil {
			t.Fatalf("sudo-exe chown failed: %v\n%s", err, out)
		}
		var resp struct {
			VMName   string `json:"vm_name"`
			OldOwner string `json:"old_owner"`
			NewOwner string `json:"new_owner"`
			Status   string `json:"status"`
		}
		if err := json.Unmarshal(out, &resp); err != nil {
			t.Fatalf("invalid json: %v\n%s", err, out)
		}
		if resp.Status != "chowned" || resp.VMName != box {
			t.Fatalf("unexpected response: %+v", resp)
		}
		if resp.OldOwner != donorID || resp.NewOwner != recipientID {
			t.Fatalf("ownership ids: got old=%s new=%s, want old=%s new=%s", resp.OldOwner, resp.NewOwner, donorID, recipientID)
		}
		chowned.Store(true)
	})

	t.Run("NewOwnerSeesVM", func(t *testing.T) {
		noGolden(t)
		needsChown(t)
		repl := sshToExeDev(t, recipientKeyFile)
		defer repl.Disconnect()
		repl.SendLine("ls")
		repl.Want(box)
		repl.WantPrompt()
	})

	t.Run("OldOwnerLostVM", func(t *testing.T) {
		noGolden(t)
		needsChown(t)
		repl := sshToExeDev(t, donorKeyFile)
		defer repl.Disconnect()
		repl.SendLine("ls")
		repl.Reject(box)
		repl.WantPrompt()
	})

	t.Run("SharesCleared", func(t *testing.T) {
		noGolden(t)
		needsChown(t)
		// The donor's original share with bystander (and pending share) should be gone.
		// After chown the donor no longer owns the VM, so `share show` should either
		// fail to find the VM or return empty share lists. We test that no shared email
		// addresses from before the chown survive on the new owner's side.
		out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), recipientKeyFile, "share", "show", box, "--json")
		if err != nil {
			t.Fatalf("share show failed: %v\n%s", err, out)
		}
		if strings.Contains(string(out), bystanderEmail) {
			t.Errorf("bystander share survived chown:\n%s", out)
		}
		if strings.Contains(string(out), "pendinguser@test-chown.example") {
			t.Errorf("pending share survived chown:\n%s", out)
		}
	})

	t.Run("NewOwnerCanSSH", func(t *testing.T) {
		noGolden(t)
		needsChown(t)
		pty := sshToBox(t, box, recipientKeyFile)
		pty.SendLine("echo chown-ok")
		pty.Want("chown-ok")
		pty.Disconnect()
	})

	t.Run("MetadataGateway", func(t *testing.T) {
		noGolden(t)
		needsChown(t)
		// Smoke-test that the LLM gateway inside the VM still reaches exed — this
		// requires the ctrhost -> exed path to know the VM's (now-new) owner for
		// per-account credit routing.
		out, err := boxSSHCommand(t, box, recipientKeyFile, "curl", "--max-time", "10", "-s", "-o", "/dev/null", "-w", "%{http_code}", "http://169.254.169.254/gateway/llm/ready").CombinedOutput()
		if err != nil {
			t.Fatalf("curl failed: %v\n%s", err, out)
		}
		code := strings.TrimSpace(string(out))
		if code != "200" {
			t.Fatalf("gateway /ready: expected 200, got %s", code)
		}
	})

	t.Run("CgroupReparented", func(t *testing.T) {
		noGolden(t)
		needsChown(t)
		ctx := Env.context(t)
		// Tests use a single exelet. If this ever changes, select by box.Ctrhost.
		if len(Env.servers.Exelets) != 1 {
			t.Skipf("multi-exelet test topology not supported here (%d exelets)", len(Env.servers.Exelets))
		}
		exeletClient := Env.servers.Exelets[0].Client()
		exelet := Env.servers.Exelets[0]
		containerID := instanceIDByName(t, ctx, exeletClient, box)

		sanitize := func(s string) string { return strings.ReplaceAll(s, "/", "_") }
		wantSlice := fmt.Sprintf("exelet.slice/%s.slice/vm-%s.scope", sanitize(recipientID), containerID)
		notWantSlice := fmt.Sprintf("exelet.slice/%s.slice/vm-%s.scope", sanitize(donorID), containerID)

		// Resource manager polls every ~30s; give it two full cycles plus slack.
		deadline := time.Now().Add(90 * time.Second)
		var lastOut string
		for time.Now().Before(deadline) {
			out, _ := exelet.Exec(ctx, fmt.Sprintf(
				"ls -d /sys/fs/cgroup/exelet.slice/*/vm-%s.scope 2>/dev/null || true",
				containerID))
			lastOut = strings.TrimSpace(string(out))
			if strings.Contains(lastOut, wantSlice) && !strings.Contains(lastOut, notWantSlice) {
				return
			}
			time.Sleep(1 * time.Second)
		}
		t.Fatalf("cgroup never reparented: want path containing %q, got %q", wantSlice, lastOut)
	})

	// Cleanup
	t.Run("Cleanup", func(t *testing.T) {
		noGolden(t)
		needsChown(t)
		repl := sshToExeDev(t, recipientKeyFile)
		defer repl.Disconnect()
		repl.SendLine("rm " + box)
		repl.Want("deleted")
		repl.WantPrompt()
	})
}
