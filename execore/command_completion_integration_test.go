package execore

import (
	"testing"

	"exe.dev/exedb"
	"exe.dev/exemenu"
	"exe.dev/tslog"
	"github.com/stretchr/testify/assert"
)

func TestCompletionIntegration(t *testing.T) {
	t.Parallel()
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
			name:     "complete r commands",
			line:     "r",
			cursor:   1,
			expected: []string{"rm", "restart", "rename"},
		},
		{
			name:     "complete with space - list commands",
			line:     "",
			cursor:   0,
			expected: []string{"help", "doc", "ls", "new", "rm", "restart", "rename", "share", "whoami", "ssh-key", "shelley", "browser", "exit"},
		},
		{
			name:     "complete rm with space - should use box completer (but no containers in test)",
			line:     "rm ",
			cursor:   3,
			expected: nil, // No containers available in test mode
		},
		{
			name:     "complete ls with space - should use box completer (but no containers in test)",
			line:     "ls ",
			cursor:   3,
			expected: nil, // No containers available in test mode
		},
		{
			name:     "complete help with partial command name",
			line:     "help ss",
			cursor:   7,
			expected: []string{"ssh", "ssh-key"},
		},
		{
			name:     "complete help with space - shows all commands",
			line:     "help ",
			cursor:   5,
			expected: []string{"help", "doc", "ls", "new", "rm", "restart", "rename", "share", "whoami", "ssh-key", "shelley", "browser", "exit", "ssh"},
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

func TestLongestCommonPrefix(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		input    []string
		expected string
	}{
		{name: "empty", input: nil, expected: ""},
		{name: "single", input: []string{"hello"}, expected: "hello"},
		{name: "common prefix", input: []string{"gbench1", "gbench2"}, expected: "gbench"},
		{name: "no common prefix", input: []string{"alpha", "beta"}, expected: ""},
		{name: "exact match", input: []string{"same", "same"}, expected: "same"},
		{name: "one is prefix of another", input: []string{"ssh", "ssh-key"}, expected: "ssh"},
		{name: "three items", input: []string{"restart", "rename", "remove"}, expected: "re"},
		{name: "multi-byte utf8 diverge", input: []string{"café", "cafê"}, expected: "caf"},
		{name: "multi-byte utf8 common", input: []string{"über-a", "über-b"}, expected: "über-"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, longestCommonPrefix(tt.input))
		})
	}
}

// TestTwoTabSequence verifies the two-phase tab completion contract at the
// CompleteCommand level: first tab expands to common prefix, second tab
// returns the full completion list.
func TestTwoTabSequence(t *testing.T) {
	t.Parallel()
	server := &Server{log: tslog.Slogger(t)}
	sshServer := NewSSHServer(server)

	cc := &exemenu.CommandContext{
		User: &exemenu.UserInfo{
			ID:    "test-user",
			Email: "test@example.com",
		},
		PublicKey: "test-key",
	}

	// Simulate: user types "help ss" and presses tab.
	line1 := "help ss"
	pos1 := 7
	completions := sshServer.commands.CompleteCommand(line1, pos1, cc)
	assert.ElementsMatch(t, []string{"ssh", "ssh-key"}, completions)

	prefix := longestCommonPrefix(completions)
	assert.Equal(t, "ssh", prefix)

	// First tab: prefix "ssh" > typed "ss", so expand to "help ssh".
	wordStart := pos1
	for wordStart > 0 && line1[wordStart-1] != ' ' && line1[wordStart-1] != '\t' {
		wordStart--
	}
	currentWord := line1[wordStart:pos1]
	assert.Equal(t, "ss", currentWord)
	assert.Greater(t, len(prefix), len(currentWord), "prefix should extend beyond typed word")

	expandedLine := line1[:wordStart] + prefix + line1[pos1:]
	expandedPos := wordStart + len(prefix)
	assert.Equal(t, "help ssh", expandedLine)
	assert.Equal(t, 8, expandedPos)

	// Second tab: same completions for expanded input.
	completions2 := sshServer.commands.CompleteCommand(expandedLine, expandedPos, cc)
	assert.ElementsMatch(t, []string{"ssh", "ssh-key"}, completions2)

	// After expansion, the prefix equals the current word — showCompletions path.
	wordStart2 := expandedPos
	for wordStart2 > 0 && expandedLine[wordStart2-1] != ' ' && expandedLine[wordStart2-1] != '\t' {
		wordStart2--
	}
	currentWord2 := expandedLine[wordStart2:expandedPos]
	prefix2 := longestCommonPrefix(completions2)
	assert.Equal(t, "ssh", currentWord2)
	assert.Equal(t, "ssh", prefix2)
	assert.Equal(t, len(prefix2), len(currentWord2), "prefix should NOT extend beyond typed word — showCompletions path")
}

// TestTwoTabSequenceMidCursor verifies that prefix expansion preserves
// text after the cursor (mid-line editing).
func TestTwoTabSequenceMidCursor(t *testing.T) {
	t.Parallel()
	server := &Server{log: tslog.Slogger(t)}
	sshServer := NewSSHServer(server)

	cc := &exemenu.CommandContext{
		User: &exemenu.UserInfo{
			ID:    "test-user",
			Email: "test@example.com",
		},
		PublicKey: "test-key",
	}

	// Simulate: user types "help ss extra" with cursor on "ss" (pos=7).
	line := "help ss extra"
	pos := 7
	completions := sshServer.commands.CompleteCommand(line, pos, cc)
	assert.ElementsMatch(t, []string{"ssh", "ssh-key"}, completions)

	prefix := longestCommonPrefix(completions)
	assert.Equal(t, "ssh", prefix)

	// Prefix expansion should replace "ss" with "ssh" and keep " extra".
	wordStart := pos
	for wordStart > 0 && line[wordStart-1] != ' ' && line[wordStart-1] != '\t' {
		wordStart--
	}
	expandedLine := line[:wordStart] + prefix + line[pos:]
	expandedPos := wordStart + len(prefix)
	assert.Equal(t, "help ssh extra", expandedLine)
	assert.Equal(t, 8, expandedPos)
}

// TestApplySingleCompletion tests the single completion logic
func TestApplySingleCompletion(t *testing.T) {
	t.Parallel()
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
