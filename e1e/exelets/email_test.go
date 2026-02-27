package exelets

import (
	"encoding/json"
	"fmt"
	"testing"
)

func TestEmail(t *testing.T) {
	t.Parallel()

	pty, _, keyFile, email := register(t)
	boxName := makeBox(t, pty, keyFile, email)
	pty.Disconnect()
	defer deleteBox(t, boxName, keyFile)

	t.Run("send", func(t *testing.T) {
		subject := "Test from " + boxName
		const body = "This is a test email from the VM."

		cmd := fmt.Sprintf(`curl -s -X POST http://169.254.169.254/gateway/email/send -H "Content-Type: application/json" -d '{"to":"%s","subject":"%s","body":"%s"}'`, email, subject, body)
		out, err := serverEnv.BoxSSHCommand(t.Context(), boxName, keyFile, cmd).CombinedOutput()
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
			t.Errorf("email send failed: %s", resp.Error)
		}

		// Verify email was received
		email, err := serverEnv.Email.WaitForEmail(email)
		if err != nil {
			t.Fatalf("failed to receive email: %v", err)
		}

		if email.Subject != subject {
			t.Errorf("got subject %q, want %q", email.Subject, subject)
		}
		if email.Body != body {
			t.Errorf("got body %q, want %q", email.Body, body)
		}
	})
}
