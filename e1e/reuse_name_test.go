package e1e

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"exe.dev/e1e/testinfra"
)

// TestReuseDeletedVMName verifies that a VM name can be reused after deletion
// when creating through the web flow (POST /create-vm).
//
// The bug (issue #167): startBoxCreation keeps a CreationStream in memory
// after a web-initiated creation completes. If the user deletes the VM and
// POSTs /create-vm with the same name, the stale done stream causes
// startBoxCreation to return early ("already in progress"), silently
// skipping the new creation.
//
// This test creates a VM via the web endpoint so that a CreationStream is
// established, deletes it, then creates again via the same endpoint.
// Without the fix the second creation never starts and the box never
// appears in the REPL ls output.
func TestReuseDeletedVMName(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 1) // at most 1 VM alive at a time
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	// Register via SSH (standard e1e pattern) to get auth cookies.
	pty, cookies, keyFile, _ := registerForExeDev(t)
	pty.Disconnect()

	host := boxName(t)
	base := fmt.Sprintf("http://localhost:%d", Env.HTTPPort())
	client := newClientWithCookies(t, cookies)

	// 1. Create VM via web POST /create-vm.
	//    This establishes a CreationStream in the server keyed by (userID, host).
	form := url.Values{}
	form.Set("hostname", host)
	resp, err := client.PostForm(base+"/create-vm", form)
	if err != nil {
		t.Fatalf("POST /create-vm: %v", err)
	}
	resp.Body.Close()
	// The default client follows the 303 redirect to the dashboard, so we expect 200.
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /create-vm: status %d", resp.StatusCode)
	}
	// Wait for the VM to be fully created and SSH-accessible.
	waitForSSH(t, host, keyFile)

	// Wait for the web creation goroutine to finish. waitForSSH only
	// confirms the container's SSH port is up; the background goroutine
	// in startBoxCreation may still be running (updateBoxWithContainer,
	// auto-routing, etc). Deleting before it finishes removes the DB row
	// out from under it, causing "sql: no rows in result set".
	waitForBoxRunning(t, keyFile, host)

	// 2. Delete the VM via SSH REPL.
	//    The CreationStream remains in server memory (done=true) because the
	//    cleanup timer hasn't fired yet (10 min idle timeout, 5 min tick).
	pty = sshToExeDev(t, keyFile)
	pty.deleteBox(host)
	pty.Disconnect()

	// Confirm the box is gone from the REPL ls output before re-creating.
	waitForBoxGone(t, keyFile, host)

	// 3. Re-create a VM with the same name via web.
	//    Before the fix, startBoxCreation found the stale done stream and
	//    returned early, silently skipping creation.
	resp, err = client.PostForm(base+"/create-vm", form)
	if err != nil {
		t.Fatalf("POST /create-vm (reuse): %v", err)
	}
	resp.Body.Close()
	// The default client follows the 303 redirect to the dashboard, so we expect 200.
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /create-vm (reuse): status %d", resp.StatusCode)
	}

	// Verify the box was actually re-created by polling the REPL ls output.
	// If the bug is present, startBoxCreation returned early and no VM was
	// created, so the box never appears and this times out.
	waitForBoxInLS(t, keyFile, host)

	// Wait for the VM to be fully running so the background creation
	// goroutine finishes cleanly before we tear down the box.
	waitForSSH(t, host, keyFile)

	// Clean up.
	cleanupBox(t, keyFile, host)
}

// waitForBoxRunning polls "ls --json" until the box reaches "running" status.
// This ensures the web creation goroutine has finished updateBoxWithContainer.
func waitForBoxRunning(t *testing.T, keyFile, boxName string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	defer cancel()
	type vmList struct {
		VMs []struct {
			Name   string `json:"vm_name"`
			Status string `json:"status"`
		} `json:"vms"`
	}
	for {
		result, err := testinfra.RunParseExeDevJSON[vmList](ctx, Env.servers, keyFile, "ls", "--json")
		if err == nil {
			for _, vm := range result.VMs {
				if vm.Name == boxName && vm.Status == "running" {
					return
				}
			}
		}
		if ctx.Err() != nil {
			t.Fatalf("timed out waiting for box %q to reach running status", boxName)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// waitForBoxInLS polls "ls" via the SSH REPL until the given box name appears.
func waitForBoxInLS(t *testing.T, keyFile, boxName string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	defer cancel()
	for {
		out, err := Env.servers.RunExeDevSSHCommand(ctx, keyFile, "ls")
		if err == nil && strings.Contains(string(out), boxName) {
			return
		}
		if ctx.Err() != nil {
			t.Fatalf("timed out waiting for box %q in ls output (last output: %s)", boxName, out)
		}
		time.Sleep(2 * time.Second)
	}
}

// waitForBoxGone polls "ls" via the SSH REPL until the given box name disappears.
func waitForBoxGone(t *testing.T, keyFile, boxName string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	for {
		out, err := Env.servers.RunExeDevSSHCommand(ctx, keyFile, "ls")
		if err == nil && !strings.Contains(string(out), boxName) {
			return
		}
		if ctx.Err() != nil {
			t.Fatalf("timed out waiting for box %q to disappear from ls (last output: %s)", boxName, out)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
