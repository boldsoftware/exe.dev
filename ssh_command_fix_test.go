package exe

import (
	"testing"

	"github.com/anmitsu/go-shlex"
)

// TestJupyterCommandFix tests the specific fix for the jupyter notebook command
func TestJupyterCommandFix(t *testing.T) {
	t.Parallel()

	// This is the exact command from the user's issue
	problematicCommand := `new --image=quay.io/jupyter/datascience-notebook --command="start-notebook.py --IdentityProvider.token=''"`

	// Parse using the new shlex approach (what SSH server now uses)
	parts, err := shlex.Split(problematicCommand, true)
	if err != nil {
		t.Fatalf("Failed to parse jupyter command: %v", err)
	}

	// Verify we get exactly 3 parts
	if len(parts) != 3 {
		t.Fatalf("Expected 3 parts, got %d: %v", len(parts), parts)
	}

	// Verify the parts are correct
	expectedParts := []string{
		"new",
		"--image=quay.io/jupyter/datascience-notebook",
		"--command=start-notebook.py --IdentityProvider.token=''",
	}

	for i, expected := range expectedParts {
		if parts[i] != expected {
			t.Errorf("Part %d: expected %q, got %q", i, expected, parts[i])
		}
	}

	t.Log("✓ Jupyter notebook command parsing fixed!")
	t.Logf("✓ Command parses into %d correct parts:", len(parts))
	for i, part := range parts {
		t.Logf("  [%d]: %s", i, part)
	}
}

// TestOtherQuotedCommands tests various other quoted command scenarios
func TestOtherQuotedCommands(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		command  string
		expected []string
	}{
		{
			name:     "simple command",
			command:  "new --image=ubuntu",
			expected: []string{"new", "--image=ubuntu"},
		},
		{
			name:     "single quotes",
			command:  `new --image=ubuntu --command='echo hello'`,
			expected: []string{"new", "--image=ubuntu", "--command=echo hello"},
		},
		{
			name:     "double quotes",
			command:  `new --image=ubuntu --command="echo hello world"`,
			expected: []string{"new", "--image=ubuntu", "--command=echo hello world"},
		},
		{
			name:     "nested quotes",
			command:  `new --image=ubuntu --command='echo "nested quotes"'`,
			expected: []string{"new", "--image=ubuntu", "--command=echo \"nested quotes\""},
		},
		{
			name:     "complex jupyter command",
			command:  `new --name="my-notebook" --image=jupyter/scipy-notebook --command="start-notebook.py --ip=0.0.0.0 --port=8888"`,
			expected: []string{"new", "--name=my-notebook", "--image=jupyter/scipy-notebook", "--command=start-notebook.py --ip=0.0.0.0 --port=8888"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parts, err := shlex.Split(tt.command, true)
			if err != nil {
				t.Fatalf("Failed to parse command %q: %v", tt.command, err)
			}

			if len(parts) != len(tt.expected) {
				t.Errorf("Expected %d parts, got %d", len(tt.expected), len(parts))
				t.Errorf("Expected: %v", tt.expected)
				t.Errorf("Got: %v", parts)
				return
			}

			for i, expected := range tt.expected {
				if parts[i] != expected {
					t.Errorf("Part %d: expected %q, got %q", i, expected, parts[i])
				}
			}
		})
	}
}
