package exe

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseCompletionInput(t *testing.T) {
	tests := []struct {
		name                string
		line                string
		expectedWords       []string
		expectedCurrentWord string
		expectedPosition    int
	}{
		{
			name:                "empty line",
			line:                "",
			expectedWords:       []string{},
			expectedCurrentWord: "",
			expectedPosition:    0,
		},
		{
			name:                "single word no space",
			line:                "list",
			expectedWords:       []string{},
			expectedCurrentWord: "list",
			expectedPosition:    0,
		},
		{
			name:                "single word with space",
			line:                "list ",
			expectedWords:       []string{"list"},
			expectedCurrentWord: "",
			expectedPosition:    1,
		},
		{
			name:                "partial second word",
			line:                "start my",
			expectedWords:       []string{"start"},
			expectedCurrentWord: "my",
			expectedPosition:    1,
		},
		{
			name:                "complete command with space",
			line:                "start mybox ",
			expectedWords:       []string{"start", "mybox"},
			expectedCurrentWord: "",
			expectedPosition:    2,
		},
		{
			name:                "quoted argument",
			line:                `start "my box" `,
			expectedWords:       []string{"start", "my box"},
			expectedCurrentWord: "",
			expectedPosition:    2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			words, currentWord, position := parseCompletionInput(tt.line)
			assert.Equal(t, tt.expectedWords, words, "words mismatch")
			assert.Equal(t, tt.expectedCurrentWord, currentWord, "current word mismatch")
			assert.Equal(t, tt.expectedPosition, position, "position mismatch")
		})
	}
}

func TestCompleteCommandName(t *testing.T) {
	// Create a simple command tree for testing
	commands := &CommandTree{
		Commands: []*Command{
			{
				Name:    "list",
				Aliases: []string{"ls"},
			},
			{
				Name: "start",
			},
			{
				Name: "stop",
			},
			{
				Name:    "help",
				Aliases: []string{"?"},
			},
		},
	}

	cc := &CommandContext{} // Empty context for basic completion

	tests := []struct {
		name     string
		prefix   string
		expected []string
	}{
		{
			name:     "empty prefix",
			prefix:   "",
			expected: []string{"list", "ls", "start", "stop", "help", "?"},
		},
		{
			name:     "prefix 's'",
			prefix:   "s",
			expected: []string{"start", "stop"},
		},
		{
			name:     "prefix 'l'",
			prefix:   "l",
			expected: []string{"list", "ls"},
		},
		{
			name:     "no matches",
			prefix:   "xyz",
			expected: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			compCtx := &CompletionContext{
				CurrentWord: tt.prefix,
				Position:    0,
			}

			result := commands.completeCommandName(compCtx, cc)
			assert.ElementsMatch(t, tt.expected, result)
		})
	}
}

func TestCompleteCommand(t *testing.T) {
	// Create a command tree with some test commands
	commands := &CommandTree{
		Commands: []*Command{
			{
				Name: "list",
			},
			{
				Name: "start",
				CompleterFunc: func(compCtx *CompletionContext, cc *CommandContext) []string {
					// Mock box name completion
					return []string{"box1", "box2", "mybox"}
				},
			},
			{
				Name: "help",
				Subcommands: []*Command{
					{
						Name: "commands",
					},
					{
						Name: "usage",
					},
				},
			},
		},
	}

	cc := &CommandContext{}

	tests := []struct {
		name     string
		line     string
		cursor   int
		expected []string
	}{
		{
			name:     "complete command name",
			line:     "l",
			cursor:   1,
			expected: []string{"list"},
		},
		{
			name:     "complete start arguments",
			line:     "start ",
			cursor:   6,
			expected: []string{"box1", "box2", "mybox"},
		},
		{
			name:     "complete start with partial",
			line:     "start my",
			cursor:   8,
			expected: []string{"box1", "box2", "mybox"}, // completer should filter
		},
		{
			name:     "complete subcommand",
			line:     "help ",
			cursor:   5,
			expected: []string{"commands", "usage"},
		},
		{
			name:     "complete subcommand partial",
			line:     "help c",
			cursor:   6,
			expected: []string{"commands"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := commands.CompleteCommand(tt.line, tt.cursor, cc)
			assert.ElementsMatch(t, tt.expected, result)
		})
	}
}

func TestCompleteBoxNames(t *testing.T) {
	// This test would require a mock container manager
	// For now, just test the basic structure
	cc := &CommandContext{
		SSHServer: nil, // No server means no completions
	}
	compCtx := &CompletionContext{
		CurrentWord: "test",
	}

	result := CompleteBoxNames(compCtx, cc)
	assert.Nil(t, result, "Should return nil when no container manager available")
}
