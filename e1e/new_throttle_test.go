// This file contains tests for the new-throttle feature.

package e1e

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestNewThrottleGlobalBlock(t *testing.T) {
	t.Skip("evan: this seem flaky")
	t.Parallel()
	reserveVMs(t, 1)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, _, _, _ := registerForExeDev(t)
	defer pty.Disconnect()

	// Enable global throttle via debug page
	throttleURL := fmt.Sprintf("http://localhost:%d/debug/new-throttle", Env.HTTPPort())
	resp, err := http.PostForm(throttleURL, url.Values{
		"enabled":        {"true"},
		"email_patterns": {""},
		"message":        {"VM creation is temporarily disabled for testing."},
	})
	if err != nil {
		t.Fatalf("failed to set throttle: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("failed to set throttle, status: %d", resp.StatusCode)
	}

	// Try to create a box - should be blocked
	bn := boxName(t)
	pty.SendLine("new --name=" + bn)
	pty.Want("VM creation is temporarily disabled for testing.")
	pty.WantPrompt()

	// Disable throttle
	resp, err = http.PostForm(throttleURL, url.Values{
		"enabled":        {""}, // checkbox not checked = empty
		"email_patterns": {""},
		"message":        {""},
	})
	if err != nil {
		t.Fatalf("failed to clear throttle: %v", err)
	}
	resp.Body.Close()

	// Now creation should work
	boxName2 := boxName(t)
	pty.SendLine("new --name=" + boxName2)
	pty.Reject("disabled")
	pty.WantRE("Creating .*" + boxName2)
	pty.Want("Ready")
	pty.WantPrompt()

	// Cleanup
	pty.deleteBox(boxName2)
}

func TestNewThrottleEmailPattern(t *testing.T) {
	t.Skip("evan: this seem flaky")
	t.Parallel()
	reserveVMs(t, 1)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, _, keyFile, email := registerForExeDev(t)
	defer pty.Disconnect()

	// Extract domain from email for pattern matching
	parts := strings.Split(email, "@")
	if len(parts) != 2 {
		t.Fatalf("invalid email format: %s", email)
	}
	domain := parts[1]

	// Set email pattern throttle via debug page
	throttleURL := fmt.Sprintf("http://localhost:%d/debug/new-throttle", Env.HTTPPort())
	resp, err := http.PostForm(throttleURL, url.Values{
		"enabled":        {""}, // global toggle off
		"email_patterns": {".*@" + strings.ReplaceAll(domain, ".", `\.`) + "$"},
		"message":        {"Your domain is blocked from creating VMs."},
	})
	if err != nil {
		t.Fatalf("failed to set throttle: %v", err)
	}
	resp.Body.Close()

	// Try to create a box - should be blocked due to email pattern
	bn := boxName(t)
	pty.SendLine("new --name=" + bn)
	pty.Want("Your domain is blocked from creating VMs.")
	pty.WantPrompt()

	// Clear the throttle pattern
	resp, err = http.PostForm(throttleURL, url.Values{
		"enabled":        {""},
		"email_patterns": {""},
		"message":        {""},
	})
	if err != nil {
		t.Fatalf("failed to clear throttle: %v", err)
	}
	resp.Body.Close()

	// Now creation should work
	boxName2 := boxName(t)
	pty.SendLine("new --name=" + boxName2)
	pty.Reject("blocked")
	pty.WantRE("Creating .*" + boxName2)
	pty.Want("Ready")
	pty.WantPrompt()

	// Cleanup
	pty.deleteBox(boxName2)

	// Suppress unused variable warnings
	_ = keyFile
}

func TestNewThrottleDebugPageJSON(t *testing.T) {
	t.Skip("evan: this seem flaky")
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	// Test the JSON endpoint of the debug page
	throttleURL := fmt.Sprintf("http://localhost:%d/debug/new-throttle?format=json", Env.HTTPPort())
	resp, err := http.Get(throttleURL)
	if err != nil {
		t.Fatalf("failed to get throttle config: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read response: %v", err)
	}

	// Should contain JSON fields
	bodyStr := string(body)
	if !strings.Contains(bodyStr, "enabled") {
		t.Errorf("expected 'enabled' in JSON response, got: %s", bodyStr)
	}
	if !strings.Contains(bodyStr, "email_patterns") {
		t.Errorf("expected 'email_patterns' in JSON response, got: %s", bodyStr)
	}
	if !strings.Contains(bodyStr, "message") {
		t.Errorf("expected 'message' in JSON response, got: %s", bodyStr)
	}
}

func TestNewThrottleDefaultMessage(t *testing.T) {
	t.Skip("evan: this seem flaky")
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, _, _, _ := registerForExeDev(t)
	defer pty.Disconnect()

	// Enable global throttle with no custom message
	throttleURL := fmt.Sprintf("http://localhost:%d/debug/new-throttle", Env.HTTPPort())
	resp, err := http.PostForm(throttleURL, url.Values{
		"enabled":        {"true"},
		"email_patterns": {""},
		"message":        {""}, // empty message should use default
	})
	if err != nil {
		t.Fatalf("failed to set throttle: %v", err)
	}
	resp.Body.Close()

	// Try to create a box - should see default message
	bn := boxName(t)
	pty.SendLine("new --name=" + bn)
	pty.Want("VM creation is temporarily disabled.")
	pty.WantPrompt()

	// Cleanup - disable throttle
	resp, err = http.PostForm(throttleURL, url.Values{
		"enabled":        {""},
		"email_patterns": {""},
		"message":        {""},
	})
	if err != nil {
		t.Fatalf("failed to clear throttle: %v", err)
	}
	resp.Body.Close()
}
