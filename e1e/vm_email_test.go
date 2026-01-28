package e1e

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"exe.dev/e1e/testinfra"
)

// TestVMEmail tests the VM email sending feature via the metadata service.
func TestVMEmail(t *testing.T) {
	t.Parallel()
	e1eTestsOnlyRunOnce(t)
	noGolden(t) // Email content varies

	pty, _, keyFile, userEmail := registerForExeDev(t)
	box := newBox(t, pty, testinfra.BoxOpts{Command: "/bin/bash"})
	pty.disconnect()
	waitForSSH(t, box, keyFile)

	// Drain the box creation email before running tests.
	if _, err := Env.servers.Email.WaitForEmail(userEmail); err != nil {
		t.Fatalf("failed to drain box creation email: %v", err)
	}

	t.Run("send_to_owner_success", func(t *testing.T) {
		// Send email to self (the VM owner)
		subject := fmt.Sprintf("Test from %s", box)
		body := "This is a test email from the VM."

		cmd := fmt.Sprintf(`curl -s -X POST http://169.254.169.254/email/send -H "Content-Type: application/json" -d '{"to":"%s","subject":"%s","body":"%s"}'`, userEmail, subject, body)
		out, err := boxSSHShell(t, box, keyFile, cmd).CombinedOutput()
		if err != nil {
			t.Fatalf("curl failed: %v\n%s", err, out)
		}

		var resp struct {
			Success bool   `json:"success"`
			Error   string `json:"error"`
		}
		if err := json.Unmarshal(out, &resp); err != nil {
			t.Fatalf("failed to parse response: %v\n%s", err, out)
		}

		if !resp.Success {
			t.Errorf("expected success, got error: %s", resp.Error)
		}

		// Verify email was received
		email, err := Env.servers.Email.WaitForEmail(userEmail)
		if err != nil {
			t.Fatalf("failed to receive email: %v", err)
		}

		if email.Subject != subject {
			t.Errorf("expected subject %q, got %q", subject, email.Subject)
		}
		if email.Body != body {
			t.Errorf("expected body %q, got %q", body, email.Body)
		}
	})

	t.Run("reject_non_owner_recipient", func(t *testing.T) {
		cmd := `curl -s -X POST http://169.254.169.254/email/send -H "Content-Type: application/json" -d '{"to":"other@example.com","subject":"Test","body":"Test body"}'`
		out, err := boxSSHShell(t, box, keyFile, cmd).CombinedOutput()
		if err != nil {
			t.Fatalf("curl failed: %v\n%s", err, out)
		}

		var resp struct {
			Success bool   `json:"success"`
			Error   string `json:"error"`
		}
		if err := json.Unmarshal(out, &resp); err != nil {
			t.Fatalf("failed to parse response: %v\n%s", err, out)
		}

		if resp.Success {
			t.Error("expected failure when sending to non-owner")
		}
		if !strings.Contains(resp.Error, "owner") {
			t.Errorf("expected owner-related error, got: %s", resp.Error)
		}
	})

	t.Run("reject_missing_fields", func(t *testing.T) {
		// Missing subject
		cmd := fmt.Sprintf(`curl -s -X POST http://169.254.169.254/email/send -H "Content-Type: application/json" -d '{"to":"%s","body":"Test body"}'`, userEmail)
		out, err := boxSSHShell(t, box, keyFile, cmd).CombinedOutput()
		if err != nil {
			t.Fatalf("curl failed: %v\n%s", err, out)
		}

		var resp struct {
			Success bool   `json:"success"`
			Error   string `json:"error"`
		}
		if err := json.Unmarshal(out, &resp); err != nil {
			t.Fatalf("failed to parse response: %v\n%s", err, out)
		}

		if resp.Success {
			t.Error("expected failure when subject is missing")
		}
		if !strings.Contains(resp.Error, "subject") {
			t.Errorf("expected subject-related error, got: %s", resp.Error)
		}
	})

	t.Run("reject_invalid_json", func(t *testing.T) {
		cmd := `curl -s -X POST http://169.254.169.254/email/send -H "Content-Type: application/json" -d 'not valid json'`
		out, err := boxSSHShell(t, box, keyFile, cmd).CombinedOutput()
		if err != nil {
			t.Fatalf("curl failed: %v\n%s", err, out)
		}

		var resp struct {
			Success bool   `json:"success"`
			Error   string `json:"error"`
		}
		if err := json.Unmarshal(out, &resp); err != nil {
			t.Fatalf("failed to parse response: %v\n%s", err, out)
		}

		if resp.Success {
			t.Error("expected failure for invalid JSON")
		}
		if !strings.Contains(resp.Error, "JSON") {
			t.Errorf("expected JSON-related error, got: %s", resp.Error)
		}
	})

	t.Run("get_method_not_allowed", func(t *testing.T) {
		out, err := boxSSHCommand(t, box, keyFile, "curl", "-s", "-o", "/dev/null",
			"-w", "%{http_code}",
			"http://169.254.169.254/email/send").CombinedOutput()
		if err != nil {
			t.Fatalf("curl failed: %v\n%s", err, out)
		}

		statusCode := strings.TrimSpace(string(out))
		if statusCode != "405" {
			t.Errorf("expected status 405 for GET request, got %s", statusCode)
		}
	})

	t.Run("reject_subject_too_long", func(t *testing.T) {
		// Subject limit is 200 characters
		longSubject := strings.Repeat("a", 201)
		cmd := fmt.Sprintf(`curl -s -X POST http://169.254.169.254/email/send -H "Content-Type: application/json" -d '{"to":"%s","subject":"%s","body":"test"}'`, userEmail, longSubject)
		out, err := boxSSHShell(t, box, keyFile, cmd).CombinedOutput()
		if err != nil {
			t.Fatalf("curl failed: %v\n%s", err, out)
		}

		var resp struct {
			Success bool   `json:"success"`
			Error   string `json:"error"`
		}
		if err := json.Unmarshal(out, &resp); err != nil {
			t.Fatalf("failed to parse response: %v\n%s", err, out)
		}

		if resp.Success {
			t.Error("expected failure for subject too long")
		}
		if !strings.Contains(resp.Error, "subject") || !strings.Contains(resp.Error, "200") {
			t.Errorf("expected subject length error, got: %s", resp.Error)
		}
	})

	t.Run("reject_subject_with_newline", func(t *testing.T) {
		// Test CRLF injection prevention.
		// The JSON contains an escaped newline (\n) which becomes a literal newline when parsed.
		// We use \\n in Go to produce the two characters \ and n in the string.
		payload := fmt.Sprintf(`{"to":"%s","subject":"Test\nInjected","body":"test"}`, userEmail)
		cmd := fmt.Sprintf(`curl -s -X POST http://169.254.169.254/email/send -H "Content-Type: application/json" -d '%s'`, payload)
		out, err := boxSSHShell(t, box, keyFile, cmd).CombinedOutput()
		if err != nil {
			t.Fatalf("curl failed: %v\n%s", err, out)
		}

		var resp struct {
			Success bool   `json:"success"`
			Error   string `json:"error"`
		}
		if err := json.Unmarshal(out, &resp); err != nil {
			t.Fatalf("failed to parse response: %v\n%s", err, out)
		}

		if resp.Success {
			t.Error("expected failure for subject with newline")
		}
		if !strings.Contains(resp.Error, "invalid characters") {
			t.Errorf("expected invalid characters error, got: %s", resp.Error)
		}
	})

	t.Run("reject_body_too_long", func(t *testing.T) {
		// Body limit is 64KB. Generate inside the VM to avoid shell escaping issues.
		// Use dd to generate 65537 bytes (64KB + 1)
		cmd := fmt.Sprintf(`body=$(dd if=/dev/zero bs=1 count=65537 2>/dev/null | tr '\0' 'a'); curl -s -X POST http://169.254.169.254/email/send -H "Content-Type: application/json" -d "{\"to\":\"%s\",\"subject\":\"test\",\"body\":\"$body\"}"`, userEmail)
		out, err := boxSSHShell(t, box, keyFile, cmd).CombinedOutput()
		if err != nil {
			t.Fatalf("curl failed: %v\n%s", err, out)
		}

		var resp struct {
			Success bool   `json:"success"`
			Error   string `json:"error"`
		}
		if err := json.Unmarshal(out, &resp); err != nil {
			t.Fatalf("failed to parse response: %v\n%s", err, out)
		}

		if resp.Success {
			t.Error("expected failure for body too long")
		}
		if !strings.Contains(resp.Error, "body") || !strings.Contains(resp.Error, "65536") {
			t.Errorf("expected body length error, got: %s", resp.Error)
		}
	})

	t.Run("reject_request_too_large", func(t *testing.T) {
		// Request limit is 128KB. Generate inside the VM.
		// Use dd to generate 130KB of data, pipe to curl via stdin to avoid command line limits.
		cmd := fmt.Sprintf(`(printf '{"to":"%s","subject":"test","body":"'; dd if=/dev/zero bs=1024 count=130 2>/dev/null | tr '\0' 'a'; printf '"}') | curl -s -w '\n%%{http_code}' -X POST http://169.254.169.254/email/send -H "Content-Type: application/json" -d @-`, userEmail)
		out, err := boxSSHShell(t, box, keyFile, cmd).CombinedOutput()
		if err != nil {
			t.Fatalf("curl failed: %v\n%s", err, out)
		}

		// Output format: JSON response followed by newline and status code
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		if len(lines) < 2 {
			t.Fatalf("unexpected output format: %s", out)
		}
		statusCode := lines[len(lines)-1]
		if statusCode != "413" {
			t.Errorf("expected status 413 for request too large, got %s", statusCode)
		}
	})
}
