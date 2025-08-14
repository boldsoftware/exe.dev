package exe

import (
	"bytes"
	"os"
	"testing"
	"exe.dev/sshbuf"
)

func TestSSHExecCommandParsing(t *testing.T) {
	tests := []struct {
		name    string
		command string
		expect  []string
	}{
		{
			name:    "unregistered user",
			command: "help",
			expect:  []string{"Please complete registration"},
		},
		{
			name:    "no command",
			command: "",
			expect:  []string{"No command specified"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create server with minimal setup
			server := &Server{
				devMode: "local",
			}

			// Create terminal emulator and buffer for output capture
			var outputBuf bytes.Buffer
			term, err := NewTerminalEmulator()
			if err != nil {
				t.Skipf("Could not create terminal emulator: %v", err)
			}
			defer term.Close()

			// Override the buffer for output capture
			term.buffer = &outputBuf

			mockChannel := &MockSSHChannel{
				term: term,
			}
	// Wrap the mock channel with SSHBufferedChannel
	bufferedChannel := sshbuf.New(mockChannel)

			// Create SSH exec payload (4-byte length + command string)
			cmdBytes := []byte(tt.command)
			payload := make([]byte, 4+len(cmdBytes))
			payload[0] = byte(len(cmdBytes) >> 24)
			payload[1] = byte(len(cmdBytes) >> 16)
			payload[2] = byte(len(cmdBytes) >> 8)
			payload[3] = byte(len(cmdBytes))
			copy(payload[4:], cmdBytes)

			// Call handleSSHExec (should fail for unregistered user, but we can test parsing)
			server.handleSSHExec(bufferedChannel, payload, "", "unregistered-fingerprint", "", "", false)

			// Check output
			rawOutput := outputBuf.String()
			output := stripANSI(rawOutput)

			for _, expected := range tt.expect {
				if !bytes.Contains([]byte(output), []byte(expected)) {
					t.Errorf("Expected output to contain %q, got: %s", expected, output)
				}
			}
		})
	}
}

func TestSSHExecWhoamiCommand(t *testing.T) {
	// Create temporary database file
	tmpDB := "/tmp/test_ssh_exec_whoami.db"
	defer func() {
		// Clean up
		if err := deleteFile(tmpDB); err != nil {
			t.Logf("Could not delete temp db: %v", err)
		}
	}()

	server, err := NewServer(":18080", "", ":12222", tmpDB, "local", []string{})
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}
	defer server.Stop()

	// Create test data
	fingerprint := "SHA256:abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"
	email := "test@example.com"
	teamName := "testteam"

	// Set up user and team
	if err := server.createUser(fingerprint, email); err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}
	if err := server.createTeam(teamName, email); err != nil {
		t.Fatalf("Failed to create team: %v", err)
	}
	if err := server.addTeamMember(fingerprint, teamName, true); err != nil {
		t.Fatalf("Failed to add team member: %v", err)
	}

	// Create terminal emulator and buffer for output capture
	var outputBuf bytes.Buffer
	term, err := NewTerminalEmulator()
	if err != nil {
		t.Skipf("Could not create terminal emulator: %v", err)
	}
	defer term.Close()

	// Override the buffer for output capture
	term.buffer = &outputBuf

	mockChannel := &MockSSHChannel{
		term: term,
	}
	// Wrap the mock channel with SSHBufferedChannel
	bufferedChannel := sshbuf.New(mockChannel)

	// Create SSH exec payload for "whoami" command (4-byte length + command string)
	cmdBytes := []byte("whoami")
	payload := make([]byte, 4+len(cmdBytes))
	payload[0] = byte(len(cmdBytes) >> 24)
	payload[1] = byte(len(cmdBytes) >> 16)
	payload[2] = byte(len(cmdBytes) >> 8)
	payload[3] = byte(len(cmdBytes))
	copy(payload[4:], cmdBytes)

	// Call handleSSHExec with registered user
	server.handleSSHExec(bufferedChannel, payload, "", fingerprint, email, "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQC7vbqaj test@example.com", true)

	// Check output
	rawOutput := outputBuf.String()
	output := stripANSI(rawOutput)

	// Verify that the output contains expected user information
	expectedStrings := []string{
		"User Information:",
		"Public Key Fingerprint:",
		"Email Address:",
		fingerprint,
		email,
		"Public Key:",
	}

	for _, expected := range expectedStrings {
		if !bytes.Contains([]byte(output), []byte(expected)) {
			t.Errorf("Expected output to contain %q, got: %s", expected, output)
		}
	}
}

func deleteFile(filename string) error {
	// Helper function to delete files, ignoring errors if file doesn't exist
	if err := os.Remove(filename); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
