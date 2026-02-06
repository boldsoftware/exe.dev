package exeweb

import (
	"testing"

	"exe.dev/stage"
)

func TestIsTerminalRequest(t *testing.T) {
	testEnv := stage.Test()

	// Test that terminal subdomains are detected correctly.
	tests := []struct {
		name     string
		host     string
		expected bool
	}{
		{"localhost terminal", "machine.xterm.exe.cloud", true},
		{"localhost terminal with port", "machine.xterm.exe.cloud:8080", true},
		{"production terminal", "machine.xterm.exe.dev", false}, // testing in dev mode
		{"regular proxy", "machine.exe.cloud", false},
		{"main domain", "localhost", false},
		{"invalid", "xterm.exe.cloud", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsTerminalRequest(&testEnv, tt.host)
			if result != tt.expected {
				t.Errorf("IsTerminalRequest(%q) = %t, want %t", tt.host, result, tt.expected)
			}
		})
	}
}
