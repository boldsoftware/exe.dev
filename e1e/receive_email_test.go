package e1e

import (
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-smtp"

	"exe.dev/e1e/testinfra"
)

// TestReceiveEmail tests the inbound email feature (share receive-email command).
// These tests focus on the REPL command behavior.
// LMTP protocol tests are in execore/lmtp_test.go.
func TestReceiveEmail(t *testing.T) {
	t.Parallel()
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, _, keyFile, _ := registerForExeDev(t)
	box := newBox(t, pty, testinfra.BoxOpts{Command: "/bin/bash"})
	pty.disconnect()
	waitForSSH(t, box, keyFile)

	t.Run("show_status_disabled_by_default", func(t *testing.T) {
		// Check initial status - should be disabled
		out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), keyFile, "share", "receive-email", box)
		if err != nil {
			t.Fatalf("command failed: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), "disabled") {
			t.Errorf("expected 'disabled' in output, got: %s", out)
		}
	})

	t.Run("enable_receive_email", func(t *testing.T) {
		// Enable receive-email
		out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), keyFile, "share", "receive-email", box, "on")
		if err != nil {
			t.Fatalf("command failed: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), "enabled") {
			t.Errorf("expected 'enabled' in output, got: %s", out)
		}
		// exe.cloud is the boxHost in test environment
		if !strings.Contains(string(out), box+".exe.cloud") {
			t.Errorf("expected email address in output, got: %s", out)
		}

		// Verify status shows enabled
		out, err = Env.servers.RunExeDevSSHCommand(Env.context(t), keyFile, "share", "receive-email", box)
		if err != nil {
			t.Fatalf("status check failed: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), "enabled") {
			t.Errorf("expected 'enabled' in status, got: %s", out)
		}
	})

	t.Run("disable_receive_email", func(t *testing.T) {
		// First enable
		_, _ = Env.servers.RunExeDevSSHCommand(Env.context(t), keyFile, "share", "receive-email", box, "on")

		// Disable receive-email
		out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), keyFile, "share", "receive-email", box, "off")
		if err != nil {
			t.Fatalf("command failed: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), "disabled") {
			t.Errorf("expected 'disabled' in output, got: %s", out)
		}

		// Verify status shows disabled
		out, err = Env.servers.RunExeDevSSHCommand(Env.context(t), keyFile, "share", "receive-email", box)
		if err != nil {
			t.Fatalf("status check failed: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), "disabled") {
			t.Errorf("expected 'disabled' in status, got: %s", out)
		}
	})

	t.Run("invalid_value_rejected", func(t *testing.T) {
		// Try invalid on/off value
		out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), keyFile, "share", "receive-email", box, "maybe")
		// Command should return an error or contain error message
		if err == nil && !strings.Contains(string(out), "invalid") && !strings.Contains(string(out), "on or off") {
			t.Errorf("expected error for invalid value, got: %s", out)
		}
	})

	t.Run("nonexistent_box_rejected", func(t *testing.T) {
		// Try on a box that doesn't exist
		out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), keyFile, "share", "receive-email", "nonexistent-box-xyz", "on")
		// Command should fail or contain error message
		if err == nil && !strings.Contains(string(out), "not found") {
			t.Errorf("expected 'not found' error, got: %s", out)
		}
	})

	t.Run("maildir_created_on_enable", func(t *testing.T) {
		// Enable and check that maildir was created
		_, err := Env.servers.RunExeDevSSHCommand(Env.context(t), keyFile, "share", "receive-email", box, "on")
		if err != nil {
			t.Fatalf("enable failed: %v", err)
		}

		// SSH into the box and check for Maildir
		out, err := boxSSHShell(t, box, keyFile, "ls -d ~/Maildir/new 2>&1 || echo 'NOTFOUND'").CombinedOutput()
		if err != nil {
			t.Fatalf("maildir check failed: %v\n%s", err, out)
		}
		outStr := string(out)
		if strings.Contains(outStr, "NOTFOUND") {
			t.Errorf("expected Maildir directories to exist after enable, got: %s", outStr)
		}
	})

	t.Run("json_output", func(t *testing.T) {
		// Test JSON output
		out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), keyFile, "share", "receive-email", box, "--json")
		if err != nil {
			t.Fatalf("command failed: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), `"email_enabled"`) {
			t.Errorf("expected JSON with email_enabled field, got: %s", out)
		}
		if !strings.Contains(string(out), `"vm_name"`) {
			t.Errorf("expected JSON with vm_name field, got: %s", out)
		}
	})

	t.Run("email_delivery_via_lmtp", func(t *testing.T) {
		// Enable receive-email on the box
		out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), keyFile, "share", "receive-email", box, "on")
		if err != nil {
			t.Fatalf("failed to enable receive-email: %v\n%s", err, out)
		}

		// Connect to LMTP socket
		sockPath := Env.servers.Exed.LMTPSocketPath
		var conn net.Conn
		for i := 0; i < 50; i++ {
			conn, err = net.Dial("unix", sockPath)
			if err == nil {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if err != nil {
			t.Fatalf("could not connect to LMTP socket at %s: %v", sockPath, err)
		}
		defer conn.Close()

		client := smtp.NewClientLMTP(conn)
		defer client.Close()

		if err := client.Hello("localhost"); err != nil {
			t.Fatalf("LHLO failed: %v", err)
		}

		if err := client.Mail("test@example.com", nil); err != nil {
			t.Fatalf("MAIL FROM failed: %v", err)
		}

		recipient := "test@" + box + ".exe.cloud"
		if err := client.Rcpt(recipient, nil); err != nil {
			t.Fatalf("RCPT TO failed: %v", err)
		}

		// Send test email
		dataCmd, err := client.Data()
		if err != nil {
			t.Fatalf("DATA failed: %v", err)
		}

		testBody := "Subject: E1E Test\r\n\r\nThis is a test email from e1e.\r\n"
		if _, err := dataCmd.Write([]byte(testBody)); err != nil {
			t.Fatalf("Write failed: %v", err)
		}

		if err := dataCmd.Close(); err != nil {
			t.Fatalf("DATA close failed: %v", err)
		}

		// Poll for email arrival
		var count string
		for i := 0; i < 50; i++ {
			checkCmd := boxSSHShell(t, box, keyFile, "ls ~/Maildir/new/ 2>/dev/null | wc -l")
			checkOut, err := checkCmd.CombinedOutput()
			if err != nil {
				t.Fatalf("failed to check Maildir: %v\n%s", err, checkOut)
			}
			count = strings.TrimSpace(string(checkOut))
			if count != "0" {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if count == "0" {
			t.Errorf("expected at least one email in Maildir/new/, got 0")
		}

		// Verify email content contains our test message
		grepCmd := boxSSHShell(t, box, keyFile, "grep -l 'E1E Test' ~/Maildir/new/*.eml 2>/dev/null")
		grepOut, err := grepCmd.CombinedOutput()
		if err != nil || strings.TrimSpace(string(grepOut)) == "" {
			t.Errorf("expected email with 'E1E Test' subject in Maildir/new/, grep output: %s", grepOut)
		}

		// Verify Delivered-To header is present and correct
		deliveredToCmd := boxSSHShell(t, box, keyFile, fmt.Sprintf("head -1 ~/Maildir/new/*.eml | grep -o 'Delivered-To: %s'", recipient))
		deliveredToOut, err := deliveredToCmd.CombinedOutput()
		if err != nil || strings.TrimSpace(string(deliveredToOut)) == "" {
			t.Errorf("expected Delivered-To header with recipient %s, got: %s (err: %v)", recipient, deliveredToOut, err)
		}
	})

	t.Run("reenable_after_manual_disable", func(t *testing.T) {
		// Enable, then disable, then enable again. Should work seamlessly.
		out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), keyFile, "share", "receive-email", box, "on")
		if err != nil {
			t.Fatalf("first enable failed: %v\n%s", err, out)
		}

		out, err = Env.servers.RunExeDevSSHCommand(Env.context(t), keyFile, "share", "receive-email", box, "off")
		if err != nil {
			t.Fatalf("disable failed: %v\n%s", err, out)
		}

		// Re-enable
		out, err = Env.servers.RunExeDevSSHCommand(Env.context(t), keyFile, "share", "receive-email", box, "on")
		if err != nil {
			t.Fatalf("re-enable failed: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), "enabled") {
			t.Errorf("expected 'enabled' in output after re-enable, got: %s", out)
		}

		// Maildir should still exist and work
		checkCmd := boxSSHShell(t, box, keyFile, "test -d ~/Maildir/new && echo ok || echo missing")
		checkOut, err := checkCmd.CombinedOutput()
		if err != nil {
			t.Fatalf("maildir check failed: %v\n%s", err, checkOut)
		}
		if strings.TrimSpace(string(checkOut)) != "ok" {
			t.Errorf("maildir should exist after re-enable, got: %s", checkOut)
		}
	})

	t.Run("reenable_after_auto_disable_with_cleanup", func(t *testing.T) {
		// First, manually simulate what happens when the auto-disable limit is hit:
		// 1. Enable email
		// 2. Directly set email_receive_enabled=0 in DB (simulating auto-disable)
		// 3. Clean up maildir
		// 4. Re-enable

		out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), keyFile, "share", "receive-email", box, "on")
		if err != nil {
			t.Fatalf("enable failed: %v\n%s", err, out)
		}

		// Disable via command (simulating auto-disable for test purposes)
		out, err = Env.servers.RunExeDevSSHCommand(Env.context(t), keyFile, "share", "receive-email", box, "off")
		if err != nil {
			t.Fatalf("disable failed: %v\n%s", err, out)
		}

		// Clean up maildir (user cleaned up their emails)
		cleanCmd := boxSSHShell(t, box, keyFile, "rm -f ~/Maildir/new/*.eml 2>/dev/null; ls ~/Maildir/new/ | wc -l")
		cleanOut, err := cleanCmd.CombinedOutput()
		if err != nil {
			t.Logf("cleanup command output: %s", cleanOut)
		}

		// Re-enable after cleanup
		out, err = Env.servers.RunExeDevSSHCommand(Env.context(t), keyFile, "share", "receive-email", box, "on")
		if err != nil {
			t.Fatalf("re-enable after cleanup failed: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), "enabled") {
			t.Errorf("expected 'enabled' after re-enable, got: %s", out)
		}
	})

	t.Run("reenable_after_auto_disable_without_cleanup", func(t *testing.T) {
		// Simulate: auto-disable happened but user hasn't cleaned up
		// Re-enable should still work (the cleanup is the user's responsibility)

		out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), keyFile, "share", "receive-email", box, "on")
		if err != nil {
			t.Fatalf("enable failed: %v\n%s", err, out)
		}

		// Put some "emails" in the maildir
		addCmd := boxSSHShell(t, box, keyFile, "touch ~/Maildir/new/test1.eml ~/Maildir/new/test2.eml")
		if addOut, err := addCmd.CombinedOutput(); err != nil {
			t.Logf("add emails output: %s", addOut)
		}

		// Disable
		out, err = Env.servers.RunExeDevSSHCommand(Env.context(t), keyFile, "share", "receive-email", box, "off")
		if err != nil {
			t.Fatalf("disable failed: %v\n%s", err, out)
		}

		// Re-enable WITHOUT cleaning up (user didn't delete old emails)
		out, err = Env.servers.RunExeDevSSHCommand(Env.context(t), keyFile, "share", "receive-email", box, "on")
		if err != nil {
			t.Fatalf("re-enable without cleanup failed: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), "enabled") {
			t.Errorf("expected 'enabled' after re-enable, got: %s", out)
		}

		// Verify the old emails are still there
		checkCmd := boxSSHShell(t, box, keyFile, "ls ~/Maildir/new/ | wc -l")
		checkOut, err := checkCmd.CombinedOutput()
		if err != nil {
			t.Fatalf("check emails failed: %v\n%s", err, checkOut)
		}
		count := strings.TrimSpace(string(checkOut))
		if count == "0" {
			t.Errorf("expected old emails to still exist, got count: %s", count)
		}

		// Clean up for other tests
		boxSSHShell(t, box, keyFile, "rm -f ~/Maildir/new/test*.eml").Run()
	})

	// Tests for LMTP recipient validation (see execore/lmtp.go Rcpt method).
	// These verify the security protections that reject invalid recipients early,
	// before reading the message body.

	t.Run("lmtp_rejects_external_domain", func(t *testing.T) {
		// Protection 2: Wrong domain suffix - only *.exe.cloud accepted
		sockPath := Env.servers.Exed.LMTPSocketPath
		var conn net.Conn
		var err error
		for range 50 {
			conn, err = net.Dial("unix", sockPath)
			if err == nil {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if err != nil {
			t.Fatalf("could not connect to LMTP socket: %v", err)
		}
		defer conn.Close()

		client := smtp.NewClientLMTP(conn)
		defer client.Close()

		if err := client.Hello("localhost"); err != nil {
			t.Fatalf("LHLO failed: %v", err)
		}

		if err := client.Mail("test@example.com", nil); err != nil {
			t.Fatalf("MAIL FROM failed: %v", err)
		}

		// Try external domain - should fail with 550
		rcptErr := client.Rcpt("user@gmail.com", nil)
		if rcptErr == nil {
			t.Errorf("expected RCPT TO to fail for external domain gmail.com")
		}
		if rcptErr != nil && !strings.Contains(rcptErr.Error(), "550") {
			t.Errorf("expected 550 error for external domain, got: %v", rcptErr)
		}
	})

	t.Run("lmtp_rejects_nested_subdomain", func(t *testing.T) {
		// Protection 3: Nested subdomains rejected (a.b.exe.cloud)
		sockPath := Env.servers.Exed.LMTPSocketPath
		var conn net.Conn
		var err error
		for range 50 {
			conn, err = net.Dial("unix", sockPath)
			if err == nil {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if err != nil {
			t.Fatalf("could not connect to LMTP socket: %v", err)
		}
		defer conn.Close()

		client := smtp.NewClientLMTP(conn)
		defer client.Close()

		if err := client.Hello("localhost"); err != nil {
			t.Fatalf("LHLO failed: %v", err)
		}

		if err := client.Mail("test@example.com", nil); err != nil {
			t.Fatalf("MAIL FROM failed: %v", err)
		}

		// Try nested subdomain - should fail with 550
		rcptErr := client.Rcpt("user@nested.sub.exe.cloud", nil)
		if rcptErr == nil {
			t.Errorf("expected RCPT TO to fail for nested subdomain nested.sub.exe.cloud")
		}
		if rcptErr != nil && !strings.Contains(rcptErr.Error(), "550") {
			t.Errorf("expected 550 error for nested subdomain, got: %v", rcptErr)
		}
	})

	t.Run("lmtp_rejects_empty_boxname", func(t *testing.T) {
		// Protection 3: Empty boxname rejected (.exe.cloud)
		sockPath := Env.servers.Exed.LMTPSocketPath
		var conn net.Conn
		var err error
		for range 50 {
			conn, err = net.Dial("unix", sockPath)
			if err == nil {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if err != nil {
			t.Fatalf("could not connect to LMTP socket: %v", err)
		}
		defer conn.Close()

		client := smtp.NewClientLMTP(conn)
		defer client.Close()

		if err := client.Hello("localhost"); err != nil {
			t.Fatalf("LHLO failed: %v", err)
		}

		if err := client.Mail("test@example.com", nil); err != nil {
			t.Fatalf("MAIL FROM failed: %v", err)
		}

		// Try empty boxname (just .exe.cloud) - should fail with 550
		// Note: The email parser may reject this as invalid syntax before we even
		// get to our domain check, which is also acceptable.
		rcptErr := client.Rcpt("user@.exe.cloud", nil)
		if rcptErr == nil {
			t.Errorf("expected RCPT TO to fail for empty boxname .exe.cloud")
		}
	})

	t.Run("lmtp_rejects_nonexistent_box", func(t *testing.T) {
		// Protection 4: Box must exist in database
		sockPath := Env.servers.Exed.LMTPSocketPath
		var conn net.Conn
		var err error
		for range 50 {
			conn, err = net.Dial("unix", sockPath)
			if err == nil {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if err != nil {
			t.Fatalf("could not connect to LMTP socket: %v", err)
		}
		defer conn.Close()

		client := smtp.NewClientLMTP(conn)
		defer client.Close()

		if err := client.Hello("localhost"); err != nil {
			t.Fatalf("LHLO failed: %v", err)
		}

		if err := client.Mail("test@example.com", nil); err != nil {
			t.Fatalf("MAIL FROM failed: %v", err)
		}

		// Try nonexistent box - should fail with 550
		rcptErr := client.Rcpt("user@nonexistent-box-xyz123.exe.cloud", nil)
		if rcptErr == nil {
			t.Errorf("expected RCPT TO to fail for nonexistent box")
		}
		if rcptErr != nil && !strings.Contains(rcptErr.Error(), "550") {
			t.Errorf("expected 550 error for nonexistent box, got: %v", rcptErr)
		}
	})

	t.Run("delivery_to_disabled_box_rejected", func(t *testing.T) {
		// Test that RCPT TO is rejected when email receive is disabled.
		// First enable, then disable, then try to deliver via LMTP.
		out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), keyFile, "share", "receive-email", box, "on")
		if err != nil {
			t.Fatalf("enable failed: %v\n%s", err, out)
		}

		out, err = Env.servers.RunExeDevSSHCommand(Env.context(t), keyFile, "share", "receive-email", box, "off")
		if err != nil {
			t.Fatalf("disable failed: %v\n%s", err, out)
		}

		// Connect to LMTP socket
		sockPath := Env.servers.Exed.LMTPSocketPath
		var conn net.Conn
		for range 50 {
			conn, err = net.Dial("unix", sockPath)
			if err == nil {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if err != nil {
			t.Fatalf("could not connect to LMTP socket: %v", err)
		}
		defer conn.Close()

		client := smtp.NewClientLMTP(conn)
		defer client.Close()

		if err := client.Hello("localhost"); err != nil {
			t.Fatalf("LHLO failed: %v", err)
		}

		if err := client.Mail("test@example.com", nil); err != nil {
			t.Fatalf("MAIL FROM failed: %v", err)
		}

		// RCPT TO should fail because email receive is disabled
		recipient := "test@" + box + ".exe.cloud"
		rcptErr := client.Rcpt(recipient, nil)
		if rcptErr == nil {
			t.Errorf("expected RCPT TO to fail for disabled box, but it succeeded")
		}
		// Should get a 550 error
		if rcptErr != nil && !strings.Contains(rcptErr.Error(), "550") {
			t.Errorf("expected 550 error, got: %v", rcptErr)
		}
	})

	t.Run("email_count_limit_auto_disable", func(t *testing.T) {
		// Test that exceeding MaxMaildirEmails triggers auto-disable.
		// In test stage, MaxMaildirEmails is 5.

		// Clean up maildir completely and verify it's empty
		cleanCmd := boxSSHShell(t, box, keyFile, "find ~/Maildir/new -type f -delete 2>/dev/null; ls ~/Maildir/new 2>/dev/null | wc -l")
		cleanOut, err := cleanCmd.CombinedOutput()
		if err != nil {
			t.Fatalf("cleanup failed: %v\n%s", err, cleanOut)
		}
		if count := strings.TrimSpace(string(cleanOut)); count != "0" {
			t.Fatalf("maildir not empty after cleanup, got %s files", count)
		}

		out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), keyFile, "share", "receive-email", box, "on")
		if err != nil {
			t.Fatalf("enable failed: %v\n%s", err, out)
		}

		// Send 6 emails via LMTP (one more than the limit of 5).
		// After the 5th email is delivered, auto-disable triggers.
		// The 6th email should fail at RCPT.
		for i := range 6 {
			// Each email needs its own connection since the session state
			// is reset after auto-disable happens.
			sockPath := Env.servers.Exed.LMTPSocketPath
			var conn net.Conn
			for range 50 {
				conn, err = net.Dial("unix", sockPath)
				if err == nil {
					break
				}
				time.Sleep(100 * time.Millisecond)
			}
			if err != nil {
				t.Fatalf("could not connect to LMTP socket on email %d: %v", i, err)
			}

			client := smtp.NewClientLMTP(conn)

			if err := client.Hello("localhost"); err != nil {
				client.Close()
				conn.Close()
				t.Fatalf("LHLO failed on email %d: %v", i, err)
			}

			if err := client.Mail("test@example.com", nil); err != nil {
				client.Close()
				conn.Close()
				t.Fatalf("MAIL FROM failed on email %d: %v", i, err)
			}

			recipient := "test@" + box + ".exe.cloud"
			if err := client.Rcpt(recipient, nil); err != nil {
				client.Close()
				conn.Close()
				// After 5 emails delivered, the 6th should fail at RCPT
				if i < 5 {
					t.Fatalf("RCPT TO failed unexpectedly on email %d: %v", i, err)
				}
				// Email 5 (index 5, the 6th email) failing is expected
				break
			}

			dataCmd, err := client.Data()
			if err != nil {
				client.Close()
				conn.Close()
				t.Fatalf("DATA failed on email %d: %v", i, err)
			}

			testBody := fmt.Sprintf("Subject: Test Email %d\r\n\r\nTest body %d.\r\n", i, i)
			if _, err := dataCmd.Write([]byte(testBody)); err != nil {
				client.Close()
				conn.Close()
				t.Fatalf("Write failed on email %d: %v", i, err)
			}

			if err := dataCmd.Close(); err != nil {
				client.Close()
				conn.Close()
				t.Fatalf("DATA close failed on email %d: %v", i, err)
			}

			client.Close()
			conn.Close()
		}

		// Verify auto-disable: status should show disabled
		var statusOut []byte
		for range 50 {
			statusOut, err = Env.servers.RunExeDevSSHCommand(Env.context(t), keyFile, "share", "receive-email", box)
			if err != nil {
				t.Fatalf("status check failed: %v\n%s", err, statusOut)
			}
			if strings.Contains(string(statusOut), "disabled") {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if !strings.Contains(string(statusOut), "disabled") {
			t.Errorf("expected email to be auto-disabled after exceeding limit, got: %s", statusOut)
		}

		// Clean up
		boxSSHShell(t, box, keyFile, "find ~/Maildir/new -type f -delete 2>/dev/null || true").Run()
	})

	t.Run("enable_fails_when_delivery_not_possible", func(t *testing.T) {
		// If delivery isn't possible, don't mark as enabled.
		// We test this by creating a file named Maildir to block directory creation.
		cleanCmd := boxSSHShell(t, box, keyFile, "rm -rf ~/Maildir")
		cleanCmd.Run()

		// Disable first
		Env.servers.RunExeDevSSHCommand(Env.context(t), keyFile, "share", "receive-email", box, "off")

		// Create a FILE named Maildir (not a directory)
		blockCmd := boxSSHShell(t, box, keyFile, "touch ~/Maildir")
		if out, err := blockCmd.CombinedOutput(); err != nil {
			t.Fatalf("failed to create blocking file: %v\n%s", err, out)
		}

		// Now try to enable - should fail because delivery will fail
		out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), keyFile, "share", "receive-email", box, "on")
		// The enable should fail with an error
		if err == nil && !strings.Contains(string(out), "failed") && !strings.Contains(string(out), "error") {
			t.Errorf("expected enable to fail when maildir is blocked, got: %s", out)
		}

		// Check that email is NOT enabled (status should show disabled)
		statusOut, _ := Env.servers.RunExeDevSSHCommand(Env.context(t), keyFile, "share", "receive-email", box)
		if strings.Contains(string(statusOut), "enabled") && !strings.Contains(string(statusOut), "disabled") {
			t.Errorf("email should not be enabled when maildir setup failed, got: %s", statusOut)
		}

		// Clean up: remove the blocking file
		boxSSHShell(t, box, keyFile, "rm -f ~/Maildir").Run()
	})

	cleanupBox(t, keyFile, box)
}
