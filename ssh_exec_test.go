package exe

import (
	"bytes"
	"testing"
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
				devMode: true,
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

			// Create SSH exec payload (4-byte length + command string)
			cmdBytes := []byte(tt.command)
			payload := make([]byte, 4+len(cmdBytes))
			payload[0] = byte(len(cmdBytes) >> 24)
			payload[1] = byte(len(cmdBytes) >> 16)
			payload[2] = byte(len(cmdBytes) >> 8)
			payload[3] = byte(len(cmdBytes))
			copy(payload[4:], cmdBytes)

			// Call handleSSHExec (should fail for unregistered user, but we can test parsing)
			server.handleSSHExec(mockChannel, payload, "", "unregistered-fingerprint", false)

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
