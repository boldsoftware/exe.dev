package execore

import (
	"testing"

	"exe.dev/stage"
)

func TestNormalizeBoxName(t *testing.T) {
	t.Parallel()

	// Create a minimal server with test env
	server := &Server{env: stage.Test()}
	ss := &SSHServer{server: server}

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "plain box name",
			input:    "connx",
			expected: "connx",
		},
		{
			name:     "full hostname with test box host",
			input:    "connx.exe.cloud",
			expected: "connx",
		},
		{
			name:     "box name with hyphen",
			input:    "my-box",
			expected: "my-box",
		},
		{
			name:     "full hostname with hyphen",
			input:    "my-box.exe.cloud",
			expected: "my-box",
		},
		{
			name:     "wrong domain leaves unchanged",
			input:    "connx.example.com",
			expected: "connx.example.com",
		},
		{
			name:     "nested subdomain leaves unchanged",
			input:    "sub.connx.exe.cloud",
			expected: "sub.connx.exe.cloud",
		},
		{
			name:     "empty string",
			input:    "",
			expected: "",
		},
		{
			name:     "just the domain",
			input:    "exe.cloud",
			expected: "exe.cloud",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ss.normalizeBoxName(tt.input)
			if result != tt.expected {
				t.Errorf("normalizeBoxName(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestNormalizeBoxNameProdEnv(t *testing.T) {
	t.Parallel()

	// Test with prod env (exe.xyz)
	server := &Server{env: stage.Prod()}
	ss := &SSHServer{server: server}

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "plain box name",
			input:    "connx",
			expected: "connx",
		},
		{
			name:     "full prod hostname",
			input:    "connx.exe.xyz",
			expected: "connx",
		},
		{
			name:     "test hostname in prod env",
			input:    "connx.exe.cloud",
			expected: "connx.exe.cloud",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ss.normalizeBoxName(tt.input)
			if result != tt.expected {
				t.Errorf("normalizeBoxName(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}
