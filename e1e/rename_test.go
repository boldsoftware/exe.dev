// This file contains e1e tests for the rename command.

package e1e

import (
	"encoding/json"
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
	reserveVMs(t, 2)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, cookies, keyFile, _ := registerForExeDev(t)

	// Run error cases that don't need VMs first
	t.Run("Usage", func(t *testing.T) {
		// Test with no arguments
		pty.SendLine("rename")
		pty.Want("usage")
		pty.WantPrompt()

		// Test with one argument
		pty.SendLine("rename onlyarg")
		pty.Want("usage")
		pty.WantPrompt()

		// Test with three arguments
		pty.SendLine("rename arg1 arg2 arg3")
		pty.Want("usage")
		pty.WantPrompt()
	})

	t.Run("NotFound", func(t *testing.T) {
		// Try to rename a non-existent VM
		pty.SendLine("rename nonexistent-vm-abc newname-xyz")
		pty.Want("not found")
		pty.WantPrompt()
	})

	// Create two VMs upfront - they'll be shared by subtests
	box1 := newBox(t, pty)
	box2 := newBox(t, pty)
	pty.Disconnect()

	waitForSSH(t, box1, keyFile)
	waitForSSH(t, box2, keyFile)

	// Run error case subtests first (these don't modify the VMs)
	t.Run("InvalidNewName", func(t *testing.T) {
		// Try to rename to an invalid name (too short)
		repl := sshToExeDev(t, keyFile)
		repl.SendLine("rename " + box1 + " abc")
		repl.Want("invalid")
		repl.WantPrompt()
		repl.Disconnect()
	})

	t.Run("NameConflict", func(t *testing.T) {
		// Try to rename box1 to box2's name
		repl := sshToExeDev(t, keyFile)
		repl.SendLine("rename " + box1 + " " + box2)
		repl.Want("already exists")
		repl.WantPrompt()
		repl.Disconnect()
	})

	t.Run("SameName", func(t *testing.T) {
		// Try to rename box1 to itself - should be a no-op
		repl := sshToExeDev(t, keyFile)
		repl.SendLine("rename " + box1 + " " + box1)
		repl.Want("already named")
		repl.WantPrompt()
		repl.Disconnect()
	})

	// Test that the metadata service returns the correct box name after rename.
	// This is critical for LLM gateway functionality - Shelley and other tools
	// inside the VM use the metadata service to identify themselves.
	// This test modifies box1, so it runs before the Success test.
	t.Run("MetadataService", func(t *testing.T) {
		// Helper to get metadata from inside the VM
		getMetadata := func(t *testing.T, box string) (name, sourceIP string) {
			t.Helper()
			out, err := boxSSHCommand(t, box, keyFile, "curl", "--max-time", "10", "-s", "http://169.254.169.254/").CombinedOutput()
			if err != nil {
				t.Fatalf("failed to get metadata: %v\n%s", err, out)
			}
			var resp struct {
				Name     string `json:"name"`
				SourceIP string `json:"source_ip"`
			}
			if err := json.Unmarshal(out, &resp); err != nil {
				t.Fatalf("failed to parse metadata response: %v\n%s", err, out)
			}
			return resp.Name, resp.SourceIP
		}

		// Helper to check LLM gateway is accessible (returns 200 on /ready)
		checkGateway := func(t *testing.T, box string) {
			t.Helper()
			out, err := boxSSHCommand(t, box, keyFile, "curl", "--max-time", "10", "-s", "-o", "/dev/null", "-w", "%{http_code}", "http://169.254.169.254/gateway/llm/ready").CombinedOutput()
			if err != nil {
				t.Fatalf("failed to check gateway: %v\n%s", err, out)
			}
			statusCode := strings.TrimSpace(string(out))
			if statusCode != "200" {
				t.Fatalf("expected gateway /ready to return 200, got %s", statusCode)
			}
		}

		// Verify metadata service returns correct name before rename
		name, _ := getMetadata(t, box1)
		if name != box1 {
			t.Fatalf("expected metadata name %q before rename, got %q", box1, name)
		}

		// Verify LLM gateway works before rename
		checkGateway(t, box1)

		// Rename the box
		newName := "box-metadata-renamed"
		repl := sshToExeDev(t, keyFile)
		repl.SendLine("rename " + box1 + " " + newName)
		repl.Want(newName)
		repl.WantPrompt()
		repl.Disconnect()

		// Wait for SSH with new name
		waitForSSH(t, newName, keyFile)

		// Verify metadata service returns the NEW name after rename.
		// This is the critical check - if exelet's config wasn't updated,
		// this will still return the old name.
		name, _ = getMetadata(t, newName)
		if name != newName {
			t.Fatalf("expected metadata name %q after rename, got %q (exelet config not updated?)", newName, name)
		}

		// Verify LLM gateway still works after rename.
		// If the metadata service returns the old name, the gateway will fail
		// with "VM not found" because the old name no longer exists in the DB.
		checkGateway(t, newName)

		// Update box1 for subsequent tests
		box1 = newName
	})

	// Now do the actual rename test (this modifies box1 again)
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
		repl.SendLine("rename " + box1 + " " + newName)
		repl.Want("-reidentified")
		repl.Want(box1)
		repl.Want(newName)
		repl.WantPrompt()
		repl.Disconnect()

		// Verify the old name no longer works in ls output
		repl = sshToExeDev(t, keyFile)
		repl.SendLine("ls")
		repl.Reject(box1)
		repl.Want(newName)
		repl.WantPrompt()
		repl.Disconnect()

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
		fixture := newProxyAuthFixture(t, box2, port, Env.servers.Exed.HTTPPort, cookies)
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
		repl.SendLine("rename " + box2 + " " + newName)
		repl.Want(newName)
		repl.WantPrompt()
		repl.Disconnect()

		// Update box2 for cleanup
		box2 = newName

		// Verify the old cookie no longer works.
		// Since the box was renamed, the cookie was invalidated and the old name
		// no longer exists. The key security property is that the cookie was
		// invalidated - if an attacker were to create a new box with the old name,
		// they couldn't use these cookies.
		// With CSRF-driven auth redirect, unauthenticated users get 307 to the
		// main domain auth page (cookie invalidated = unauthenticated).
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

		// The old cookie is invalidated, so the user is unauthenticated and gets
		// redirected to the auth page (307). The important thing is they don't
		// get 200 OK (access granted).
		if resp.StatusCode == http.StatusOK {
			t.Fatalf("stale cookie should not grant access after rename, got 200: %s", body)
		}
	})

	// Cleanup both boxes
	cleanupBox(t, keyFile, box1)
	cleanupBox(t, keyFile, box2)
}
