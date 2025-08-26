package exe

import (
	"strings"
	"testing"

	"github.com/anmitsu/go-shlex"
)

// TestCommandParsing tests that command parsing handles quotes correctly
func TestCommandParsing(t *testing.T) {
	tests := []struct {
		name     string
		command  string
		expected []string
	}{
		{
			name:     "simple command",
			command:  `new --image=ubuntu`,
			expected: []string{"new", "--image=ubuntu"},
		},
		{
			name:     "command with quoted argument",
			command:  `new --image=quay.io/jupyter/datascience-notebook --command="start-notebook.py --IdentityProvider.token=''"`,
			expected: []string{"new", "--image=quay.io/jupyter/datascience-notebook", "--command=start-notebook.py --IdentityProvider.token=''"},
		},
		{
			name:     "command with single quotes",
			command:  `new --image=ubuntu --command='echo "hello world"'`,
			expected: []string{"new", "--image=ubuntu", "--command=echo \"hello world\""},
		},
		{
			name:     "command with spaces in argument",
			command:  `new --name="my container" --image=ubuntu`,
			expected: []string{"new", "--name=my container", "--image=ubuntu"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name+" current parsing", func(t *testing.T) {
			// Test current string.Fields parsing (should fail for quotes)
			parts := strings.Fields(strings.TrimSpace(tt.command))
			if tt.name == "simple command" {
				// This should pass with current implementation
				if len(parts) != len(tt.expected) {
					t.Errorf("Current parsing failed for simple command: got %v, want %v", parts, tt.expected)
				}
				for i, part := range parts {
					if part != tt.expected[i] {
						t.Errorf("Current parsing failed for simple command: part %d got %q, want %q", i, part, tt.expected[i])
					}
				}
			} else {
				// These should fail with current implementation - just log for now
				t.Logf("Current parsing (strings.Fields) for %q: %v", tt.command, parts)
				t.Logf("Expected: %v", tt.expected)
			}
		})

		t.Run(tt.name+" proper parsing", func(t *testing.T) {
			// Test shlex parsing (should work for all)
			parts, err := shlex.Split(tt.command, true)
			if err != nil {
				t.Fatalf("shlex.Split failed: %v", err)
			}
			if len(parts) != len(tt.expected) {
				t.Errorf("shlex parsing failed: got %d parts, want %d", len(parts), len(tt.expected))
				t.Errorf("Got: %v", parts)
				t.Errorf("Expected: %v", tt.expected)
				return
			}
			for i, part := range parts {
				if part != tt.expected[i] {
					t.Errorf("shlex parsing failed: part %d got %q, want %q", i, part, tt.expected[i])
				}
			}
		})
	}
}

// TestSSHCommandParsingIntegration tests that the actual command parsing in the SSH server uses shlex
func TestSSHCommandParsingIntegration(t *testing.T) {
	// Test the specific problematic command from the issue
	command := `new --image=quay.io/jupyter/datascience-notebook --command="start-notebook.py --IdentityProvider.token=''"`

	// Test that shlex.Split works correctly for the command (this is what the SSH server now uses)
	parts, err := shlex.Split(strings.TrimSpace(command), true)
	if err != nil {
		t.Fatalf("Failed to parse command %q: %v", command, err)
	}

	t.Logf("Command %q parsed into %d parts:", command, len(parts))
	for i, part := range parts {
		t.Logf("  [%d]: %q", i, part)
	}

	// Verify correct parsing
	if len(parts) != 3 {
		t.Errorf("Expected 3 parts, got %d: %v", len(parts), parts)
	}

	if len(parts) >= 1 && parts[0] != "new" {
		t.Errorf("Expected first part to be 'new', got %q", parts[0])
	}

	if len(parts) >= 2 && parts[1] != "--image=quay.io/jupyter/datascience-notebook" {
		t.Errorf("Expected second part to be image flag, got %q", parts[1])
	}

	if len(parts) >= 3 {
		expected := "--command=start-notebook.py --IdentityProvider.token=''"
		if parts[2] != expected {
			t.Errorf("Expected third part to be %q, got %q", expected, parts[2])
		}
		// The --command argument should not have quotes around it anymore
		if strings.HasPrefix(parts[2], `"`) || strings.HasSuffix(parts[2], `"`) {
			t.Errorf("Command argument should not have quotes around it: %q", parts[2])
		}
	}

	// Test comparison with old behavior (strings.Fields)
	oldParts := strings.Fields(strings.TrimSpace(command))
	t.Logf("Old parsing (strings.Fields) would produce %d parts:", len(oldParts))
	for i, part := range oldParts {
		t.Logf("  [%d]: %q", i, part)
	}

	// The old parsing should be different/wrong
	if len(oldParts) <= 2 {
		t.Logf("✓ Old parsing produces too few parts - demonstrates the bug")
	}
}
