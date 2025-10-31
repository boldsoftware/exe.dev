package execore

import (
	"testing"

	"exe.dev/exedb"
	"exe.dev/exemenu"
	"exe.dev/tslog"
	"github.com/stretchr/testify/assert"
)

func TestCompletionIntegration(t *testing.T) {
	// Create a test SSH server with the real command tree
	server := &Server{log: tslog.Slogger(t)}
	sshServer := NewSSHServer(server)

	// Create test user context
	user := &exedb.User{
		UserID: "test-user",
		Email:  "test@example.com",
	}

	cc := &exemenu.CommandContext{
		User: &exemenu.UserInfo{
			ID:    user.UserID,
			Email: user.Email,
		},
		PublicKey: "test-key",
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
			expected: []string{"ls"},
		},
		{
			name:     "complete rm command",
			line:     "r",
			cursor:   1,
			expected: []string{"rm"},
		},
		{
			name:     "complete with space - list commands",
			line:     "",
			cursor:   0,
			expected: []string{"help", "doc", "ls", "new", "rm", "proxy", "share", "whoami", "delete-ssh-key", "browser", "exit"},
		},
		{
			name:     "complete rm with space - should use box completer (but no containers in test)",
			line:     "rm ",
			cursor:   3,
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
					for _, expected := range []string{"ls", "rm", "help", "new"} {
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
	server := &Server{}
	sshServer := NewSSHServer(server)

	tests := []struct {
		name         string
		line         string
		pos          int
		completion   string
		expectedLine string
		expectedPos  int
	}{
		{
			name:         "complete partial word",
			line:         "l",
			pos:          1,
			completion:   "ls",
			expectedLine: "ls ",
			expectedPos:  3,
		},
		{
			name:         "complete with existing text after",
			line:         "ls my existing text",
			pos:          5, // cursor at end of "my"
			completion:   "mybox",
			expectedLine: "ls mybox  existing text",
			expectedPos:  9,
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
