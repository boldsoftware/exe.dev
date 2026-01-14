// This file tests support access functionality for exe.dev support staff.

package e1e

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"testing"

	"exe.dev/e1e/testinfra"
)

// TestSupportAccess tests the support root access mechanism end-to-end.
// This verifies that exe.dev support can access a user's box when:
// 1. The support user has root_support privilege enabled
// 2. The box owner has enabled support_access_allowed on their box
//
// The test uses a single box to exercise all permutations for both SSH and HTTP proxy access.
func TestSupportAccess(t *testing.T) {
	t.Parallel()
	noGolden(t)

	// Create two users: an owner and a support user
	ownerPTY, ownerCookies, ownerKeyFile, _ := registerForExeDevWithEmail(t, "owner@test-support-access.example")
	supportPTY, supportCookies, supportKeyFile, supportEmail := registerForExeDevWithEmail(t, "support@test-support-access.example")

	// Owner creates a box
	box := newBox(t, ownerPTY, testinfra.BoxOpts{Command: "/bin/bash"})
	ownerPTY.wantPrompt()
	ownerPTY.disconnect()

	// Wait for SSH to be ready
	waitForSSH(t, box, ownerKeyFile)

	// Create a test file so we can verify we got into the box
	createTestFile := boxSSHCommand(t, box, ownerKeyFile, "echo", "support-test-marker", ">", "/home/exedev/support-test.txt")
	if out, err := createTestFile.CombinedOutput(); err != nil {
		t.Fatalf("failed to create test file: %v\n%s", err, out)
	}

	// Create index.html for HTTP proxy tests
	makeIndex := boxSSHCommand(t, box, ownerKeyFile, "echo", "support-proxy-test", ">", "/home/exedev/index.html")
	if err := makeIndex.Run(); err != nil {
		t.Fatalf("failed to create index.html: %v", err)
	}
	const boxInternalPort = 8080
	startHTTPServer(t, box, ownerKeyFile, boxInternalPort)
	httpPort := Env.servers.Exed.HTTPPort
	configureProxyRoute(t, ownerKeyFile, box, boxInternalPort, "private")

	supportBox := "support+" + box

	// Test that support user without root_support flag cannot SSH using support+ prefix
	t.Run("ssh_denied_no_root_support", func(t *testing.T) {
		cmd := boxSSHCommand(t, supportBox, supportKeyFile, "true")
		err := cmd.Run()
		if err == nil {
			t.Errorf("expected SSH to fail for support user without root_support")
		}
	})

	// Test that support user without root_support cannot access via HTTP proxy
	t.Run("proxy_denied_no_root_support", func(t *testing.T) {
		proxyAssert(t, box, proxyExpectation{
			name:     "support user cannot access box without root_support flag",
			httpPort: httpPort,
			cookies:  supportCookies,
			httpCode: http.StatusUnauthorized,
		})
	})

	// Enable root_support for the support user via debug endpoint
	t.Run("enable_root_support", func(t *testing.T) {
		enableRootSupport(t, supportEmail)
	})

	// Test that support user with root_support but box without support_access_allowed cannot SSH
	t.Run("ssh_denied_box_not_enabled", func(t *testing.T) {
		cmd := boxSSHCommand(t, supportBox, supportKeyFile, "true")
		err := cmd.Run()
		if err == nil {
			t.Errorf("expected SSH to fail when box doesn't have support_access_allowed")
		}
	})

	// Test that support user with root_support but box without support_access_allowed cannot access via HTTP proxy
	t.Run("proxy_denied_box_not_enabled", func(t *testing.T) {
		proxyAssert(t, box, proxyExpectation{
			name:     "support user cannot access box without support_access_allowed",
			httpPort: httpPort,
			cookies:  supportCookies,
			httpCode: http.StatusUnauthorized,
		})
	})

	// Owner enables support access on their box
	t.Run("enable_support_access_on_box", func(t *testing.T) {
		ownerPTY = sshToExeDev(t, ownerKeyFile)
		ownerPTY.sendLine(fmt.Sprintf("grant-support-root %s on", box))
		ownerPTY.want("exe.dev support now has root access")
		ownerPTY.wantPrompt()
		ownerPTY.disconnect()
	})

	// Test that support user can now SSH to the box using support+ prefix
	t.Run("ssh_granted", func(t *testing.T) {
		cmd := boxSSHCommand(t, supportBox, supportKeyFile, "cat", "/home/exedev/support-test.txt")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("expected SSH to succeed for support user: %v\n%s", err, out)
		}
		if got := string(out); got != "support-test-marker\n" {
			t.Errorf("unexpected output: got %q, want %q", got, "support-test-marker\n")
		}
	})

	// Test that support user can access box via HTTP proxy
	t.Run("proxy_granted", func(t *testing.T) {
		proxyAssert(t, box, proxyExpectation{
			name:     "support user can access box with root_support and support_access_allowed",
			httpPort: httpPort,
			cookies:  supportCookies,
			httpCode: http.StatusOK,
		})
	})

	// Test that owner still has normal access to their box - SSH
	t.Run("owner_ssh_unchanged", func(t *testing.T) {
		cmd := boxSSHCommand(t, box, ownerKeyFile, "cat", "/home/exedev/support-test.txt")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("expected SSH to succeed for owner: %v\n%s", err, out)
		}
		if got := string(out); got != "support-test-marker\n" {
			t.Errorf("unexpected output: got %q, want %q", got, "support-test-marker\n")
		}
	})

	// Test that owner still has normal access to their box - proxy
	t.Run("owner_proxy_unchanged", func(t *testing.T) {
		proxyAssert(t, box, proxyExpectation{
			name:     "owner still has proxy access",
			httpPort: httpPort,
			cookies:  ownerCookies,
			httpCode: http.StatusOK,
		})
	})

	// Owner revokes support access
	t.Run("revoke_support_access", func(t *testing.T) {
		ownerPTY = sshToExeDev(t, ownerKeyFile)
		ownerPTY.sendLine(fmt.Sprintf("grant-support-root %s off", box))
		ownerPTY.want("support root access")
		ownerPTY.want("revoked")
		ownerPTY.wantPrompt()
		ownerPTY.disconnect()
	})

	// Test that support user can no longer SSH to the box
	t.Run("ssh_revoked", func(t *testing.T) {
		cmd := boxSSHCommand(t, supportBox, supportKeyFile, "true")
		err := cmd.Run()
		if err == nil {
			t.Errorf("expected SSH to fail after support access was revoked")
		}
	})

	// Test that support user can no longer access box via HTTP proxy
	t.Run("proxy_revoked", func(t *testing.T) {
		proxyAssert(t, box, proxyExpectation{
			name:     "support user denied after revocation",
			httpPort: httpPort,
			cookies:  supportCookies,
			httpCode: http.StatusUnauthorized,
		})
	})

	// Cleanup
	supportPTY.disconnect()
	ownerPTY = sshToExeDev(t, ownerKeyFile)
	ownerPTY.deleteBox(box)
	ownerPTY.disconnect()
}

// enableRootSupport enables root_support for a user via the debug endpoint.
func enableRootSupport(t *testing.T, email string) {
	t.Helper()

	resp, err := http.Get(fmt.Sprintf("http://localhost:%d/debug/users?format=json", Env.servers.Exed.HTTPPort))
	if err != nil {
		t.Fatalf("failed to get users list: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected status getting users list: %d", resp.StatusCode)
	}

	type userInfo struct {
		UserID      string `json:"user_id"`
		Email       string `json:"email"`
		RootSupport bool   `json:"root_support"`
	}
	var users []userInfo
	if err := json.NewDecoder(resp.Body).Decode(&users); err != nil {
		t.Fatalf("failed to parse users JSON: %v", err)
	}

	var userID string
	for _, u := range users {
		if u.Email == email {
			userID = u.UserID
			break
		}
	}
	if userID == "" {
		t.Fatalf("user %s not found in users list", email)
	}

	form := url.Values{}
	form.Add("user_id", userID)
	form.Add("enable", "1")
	form.Add("confirm_email", email)

	postResp, err := http.PostForm(
		fmt.Sprintf("http://localhost:%d/debug/users/toggle-root-support", Env.servers.Exed.HTTPPort),
		form,
	)
	if err != nil {
		t.Fatalf("failed to enable root_support: %v", err)
	}
	defer postResp.Body.Close()

	if postResp.StatusCode != http.StatusOK && postResp.StatusCode != http.StatusSeeOther {
		body, _ := io.ReadAll(postResp.Body)
		t.Fatalf("unexpected status enabling root_support: %d, body: %s", postResp.StatusCode, body)
	}
}
