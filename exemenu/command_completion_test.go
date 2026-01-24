package exemenu

import (
	"flag"
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

func TestCompleteSubcommandWithFlags(t *testing.T) {
	// Create a command tree with subcommands that have positional args
	commands := &CommandTree{
		Commands: []*Command{
			{
				Name: "share",
				Subcommands: []*Command{
					{
						Name:              "show",
						HasPositionalArgs: true,
						FlagSetFunc: func() *flag.FlagSet {
							fs := flag.NewFlagSet("show", flag.ContinueOnError)
							fs.Bool("qr", false, "show QR code")
							fs.Bool("json", false, "output JSON")
							return fs
						},
						CompleterFunc: func(compCtx *CompletionContext, cc *CommandContext) []string {
							return []string{"vm1", "vm2", "myvm"}
						},
					},
					{
						Name:              "add",
						HasPositionalArgs: true,
						CompleterFunc: func(compCtx *CompletionContext, cc *CommandContext) []string {
							return []string{"vm1", "vm2", "myvm"}
						},
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
			name:     "complete subcommand args without flags",
			line:     "share show ",
			cursor:   11,
			expected: []string{"vm1", "vm2", "myvm"},
		},
		{
			name:     "complete subcommand args with flag before",
			line:     "share show --qr ",
			cursor:   16,
			expected: []string{"vm1", "vm2", "myvm"},
		},
		{
			name:     "complete subcommand args with flag after vm",
			line:     "share show myvm --qr ",
			cursor:   21,
			expected: []string{"vm1", "vm2", "myvm"},
		},
		{
			name:     "complete subcommand args with flag before command",
			line:     "share --json show ",
			cursor:   18,
			expected: []string{"vm1", "vm2", "myvm"},
		},
		{
			name:     "complete partial with flag present",
			line:     "share show --qr my",
			cursor:   18,
			expected: []string{"vm1", "vm2", "myvm"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := commands.CompleteCommand(tt.line, tt.cursor, cc)
			assert.ElementsMatch(t, tt.expected, result)
		})
	}
}

func TestCompleteFlagHidesHiddenFlags(t *testing.T) {
	// Create a command with both visible and hidden flags
	commands := &CommandTree{
		Commands: []*Command{
			{
				Name: "test",
				FlagSetFunc: func() *flag.FlagSet {
					fs := flag.NewFlagSet("test", flag.ContinueOnError)
					fs.String("visible", "", "this flag is visible")
					fs.String("hidden", "", "[hidden] this flag is hidden")
					fs.Bool("another-visible", false, "also visible")
					fs.Bool("another-hidden", false, "[hidden] also hidden")
					return fs
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
			name:     "complete all flags starting with --",
			line:     "test --",
			cursor:   7,
			expected: []string{"--visible", "--another-visible"},
		},
		{
			name:     "complete flags starting with --v",
			line:     "test --v",
			cursor:   8,
			expected: []string{"--visible"},
		},
		{
			name:     "hidden flags should not appear",
			line:     "test --h",
			cursor:   8,
			expected: []string{}, // --hidden should not appear
		},
		{
			name:     "complete flags starting with --a",
			line:     "test --a",
			cursor:   8,
			expected: []string{"--another-visible"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := commands.CompleteCommand(tt.line, tt.cursor, cc)
			assert.ElementsMatch(t, tt.expected, result)
		})
	}
}
