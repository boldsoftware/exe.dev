package exe

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCompletionIntegration(t *testing.T) {
	// Create a test SSH server with the real command tree
	server := &Server{
		testMode: true,
	}
	sshServer := NewSSHServer(server, nil) // nil billing for completion testing

	// Create test user and alloc context
	user := &User{
		UserID: "test-user",
		Email:  "test@example.com",
	}
	alloc := &Alloc{
		AllocID: "test-alloc",
	}

	cc := &CommandContext{
		User:      user,
		Alloc:     alloc,
		PublicKey: "test-key",
		SSHServer: sshServer,
	}

	tests := []struct {
		name     string
		line     string
		cursor   int
		expected []string
	}{
		{
			name:     "complete command names",
			line:     "l",
			cursor:   1,
			expected: []string{"list", "ls", "logs"},
		},
		{
			name:     "complete start command",
			line:     "star",
			cursor:   4,
			expected: []string{"start"},
		},
		{
			name:     "complete help command",
			line:     "?",
			cursor:   1,
			expected: []string{"?"},
		},
		{
			name:     "complete with space - list commands",
			line:     "",
			cursor:   0,
			expected: []string{"help", "?", "list", "ls", "new", "start", "stop", "delete", "logs", "diag", "diagnostics", "alloc", "billing", "whoami", "exit"},
		},
		{
			name:     "complete start with space - should use box completer (but no containers in test)",
			line:     "start ",
			cursor:   6,
			expected: nil, // No containers available in test mode
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := sshServer.commands.CompleteCommand(tt.line, tt.cursor, cc)
			if tt.expected == nil {
				assert.Nil(t, result)
			} else {
				// For command name completion, we expect all the commands to be there
				if len(tt.expected) > 10 {
					// Just check that we have a reasonable number of completions
					assert.True(t, len(result) >= 10, "Should have multiple command completions")
					// Check that expected commands are in the result
					for _, expected := range []string{"list", "start", "help", "new"} {
						assert.Contains(t, result, expected)
					}
				} else {
					assert.ElementsMatch(t, tt.expected, result)
				}
			}
		})
	}
}

// TestCompletionWithMockBoxes tests completion with mock box data
// Note: This is covered by unit tests in command_completion_test.go
// The CompleteBoxNames function is tested there with nil container manager

// TestApplySingleCompletion tests the single completion logic
func TestApplySingleCompletion(t *testing.T) {
	server := &Server{testMode: true}
	sshServer := NewSSHServer(server, nil)

	tests := []struct {
		name         string
		line         string
		pos          int
		completion   string
		expectedLine string
		expectedPos  int
	}{
		{
			name:         "complete at end of word",
			line:         "star",
			pos:          4,
			completion:   "start",
			expectedLine: "start ",
			expectedPos:  6,
		},
		{
			name:         "complete partial word",
			line:         "l",
			pos:          1,
			completion:   "list",
			expectedLine: "list ",
			expectedPos:  5,
		},
		{
			name:         "complete with existing text after",
			line:         "start my existing text",
			pos:          8, // cursor at end of "my"
			completion:   "mybox",
			expectedLine: "start mybox  existing text",
			expectedPos:  12,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			newLine, newPos := sshServer.applySingleCompletion(tt.line, tt.pos, tt.completion)
			assert.Equal(t, tt.expectedLine, newLine, "completed line should match")
			assert.Equal(t, tt.expectedPos, newPos, "new cursor position should match")
		})
	}
}
