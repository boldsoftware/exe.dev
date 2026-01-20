// This file contains e1e tests for the rename command.

package e1e

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestRename tests the rename command end-to-end.
// It creates two VMs and runs multiple subtests that share them to minimize VM creation overhead.
// The subtests are ordered so non-destructive tests run first, then the actual rename.
func TestRename(t *testing.T) {
	t.Parallel()
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, cookies, keyFile, _ := registerForExeDev(t)

	// Create two VMs upfront - they'll be shared by subtests
	box1 := newBox(t, pty)
	box2 := newBox(t, pty)
	pty.disconnect()

	waitForSSH(t, box1, keyFile)
	waitForSSH(t, box2, keyFile)

	// Run error case subtests first (these don't modify the VMs)
	t.Run("InvalidNewName", func(t *testing.T) {
		// Try to rename to an invalid name (too short)
		repl := sshToExeDev(t, keyFile)
		repl.sendLine("rename " + box1 + " abc")
		repl.want("invalid")
		repl.wantPrompt()
		repl.disconnect()
	})

	t.Run("NameConflict", func(t *testing.T) {
		// Try to rename box1 to box2's name
		repl := sshToExeDev(t, keyFile)
		repl.sendLine("rename " + box1 + " " + box2)
		repl.want("already exists")
		repl.wantPrompt()
		repl.disconnect()
	})

	t.Run("SameName", func(t *testing.T) {
		// Try to rename box1 to itself - should be a no-op
		repl := sshToExeDev(t, keyFile)
		repl.sendLine("rename " + box1 + " " + box1)
		repl.want("already named")
		repl.wantPrompt()
		repl.disconnect()
	})

	// Now do the actual rename test (this modifies box1)
	t.Run("Success", func(t *testing.T) {
		// Verify initial hostname inside the VM
		out, err := boxSSHCommand(t, box1, keyFile, "hostname").CombinedOutput()
		if err != nil {
			t.Fatalf("failed to get hostname: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), box1) {
			t.Fatalf("expected hostname to contain %q, got %q", box1, string(out))
		}

		// Rename the VM using the rename command
		newName := "box-reidentified"
		repl := sshToExeDev(t, keyFile)
		repl.sendLine("rename " + box1 + " " + newName)
		repl.want("-reidentified")
		repl.want(box1)
		repl.want(newName)
		repl.wantPrompt()
		repl.disconnect()

		// Verify the old name no longer works in ls output
		repl = sshToExeDev(t, keyFile)
		repl.sendLine("ls")
		repl.reject(box1)
		repl.want(newName)
		repl.wantPrompt()
		repl.disconnect()

		// Wait for SSH to be ready with the new name
		waitForSSH(t, newName, keyFile)

		// Verify the hostname inside the VM was updated
		out, err = boxSSHCommand(t, newName, keyFile, "hostname").CombinedOutput()
		if err != nil {
			t.Fatalf("failed to get hostname after rename: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), newName) {
			t.Fatalf("expected hostname to contain %q after rename, got %q", newName, string(out))
		}

		// Verify /etc/hostname was updated
		out, err = boxSSHCommand(t, newName, keyFile, "cat", "/etc/hostname").CombinedOutput()
		if err != nil {
			t.Fatalf("failed to read /etc/hostname: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), newName) {
			t.Fatalf("expected /etc/hostname to contain %q, got %q", newName, string(out))
		}

		// Verify /etc/hosts was updated
		out, err = boxSSHCommand(t, newName, keyFile, "cat", "/etc/hosts").CombinedOutput()
		if err != nil {
			t.Fatalf("failed to read /etc/hosts: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), newName) {
			t.Fatalf("expected /etc/hosts to contain %q, got %q", newName, string(out))
		}

		// Update box1 to newName for cleanup
		box1 = newName
	})

	// Test cookie invalidation using box2 (which hasn't been modified yet).
	// This test verifies that auth cookies are invalidated when a VM is renamed,
	// preventing a security vulnerability where an attacker could snatch the old name
	// and use lingering cookies from the original owner.
	t.Run("CookieInvalidation", func(t *testing.T) {
		// Create index.html and start HTTP server on box2
		serveIndex(t, box2, keyFile, "alive")
		configureProxyRoute(t, keyFile, box2, 8080, "private")
		port := Env.servers.Exed.HTTPPort

		// Login through proxy to get an auth cookie for this box
		fixture := newProxyAuthFixture(t, box2, port, cookies)
		jar := fixture.newJar()
		fixture.loginThroughProxy(jar)
		authCookie := fixture.authCookie(jar)

		// Verify the cookie works before rename
		client := noRedirectClient(nil)
		req, err := localhostRequestWithHostHeader("GET", fixture.proxyURL, nil)
		if err != nil {
			t.Fatalf("failed to create request: %v", err)
		}
		req.AddCookie(authCookie)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("failed to make request with cookie: %v", err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("cookie should work before rename, got status %d: %s", resp.StatusCode, body)
		}

		// Rename the VM
		oldName := box2
		newName := "box-cookietest-renamed"
		repl := sshToExeDev(t, keyFile)
		repl.sendLine("rename " + box2 + " " + newName)
		repl.want(newName)
		repl.wantPrompt()
		repl.disconnect()

		// Update box2 for cleanup
		box2 = newName

		// Verify the old cookie no longer works.
		// Since the box was renamed, the old name no longer exists, so we get 401.
		// The key security property is that the cookie was invalidated - if an attacker
		// were to create a new box with the old name, they couldn't use these cookies.
		oldBoxURL := fmt.Sprintf("http://%s.exe.cloud:%d/", oldName, port)
		req, err = localhostRequestWithHostHeader("GET", oldBoxURL, nil)
		if err != nil {
			t.Fatalf("failed to create request for old box name: %v", err)
		}
		req.AddCookie(authCookie)
		resp, err = client.Do(req)
		if err != nil {
			t.Fatalf("failed to make request with stale cookie: %v", err)
		}
		body, _ = io.ReadAll(resp.Body)
		resp.Body.Close()

		// The old box name no longer exists, so we get 401 Unauthorized
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("expected 401 for old box name after rename, got status %d: %s", resp.StatusCode, body)
		}
		if !strings.Contains(string(body), "Access required") {
			t.Fatalf("expected 'Access required' in response body, got: %s", body)
		}
	})

	// Cleanup both boxes
	cleanupBox(t, keyFile, box1)
	cleanupBox(t, keyFile, box2)
}

// TestRenameNoVM tests rename command behavior that doesn't require a VM.
// This includes usage errors and not-found errors.
func TestRenameNoVM(t *testing.T) {
	t.Parallel()
	e1eTestsOnlyRunOnce(t)

	pty, _, _, _ := registerForExeDev(t)

	t.Run("Usage", func(t *testing.T) {
		// Test with no arguments
		pty.sendLine("rename")
		pty.want("usage")
		pty.wantPrompt()

		// Test with one argument
		pty.sendLine("rename onlyarg")
		pty.want("usage")
		pty.wantPrompt()

		// Test with three arguments
		pty.sendLine("rename arg1 arg2 arg3")
		pty.want("usage")
		pty.wantPrompt()
	})

	t.Run("NotFound", func(t *testing.T) {
		// Try to rename a non-existent VM
		pty.sendLine("rename nonexistent-vm-abc newname-xyz")
		pty.want("not found")
		pty.wantPrompt()
	})

	pty.disconnect()
}
