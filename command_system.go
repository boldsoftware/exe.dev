package exe

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"regexp"
	"strings"
	"text/tabwriter"

	"github.com/gliderlabs/ssh"
	"golang.org/x/term"
)

// Command represents a single command in the command tree
type Command struct {
	Name              string
	Aliases           []string
	Description       string
	Usage             string
	FlagSetFunc       func() *flag.FlagSet // Factory to create a new FlagSet for each invocation
	Examples          []string
	Subcommands       []*Command
	HasPositionalArgs bool
	Handler           func(context.Context, *CommandContext) error
	Available         func(ctx *CommandContext) bool // nil means always available
}

func (c *Command) Help(cc *CommandContext) error {
	cc.Writeln("\r\n\033[1;33mCommand: %s\033[0m", c.Name)
	if len(c.Aliases) > 0 {
		cc.Writeln("Aliases: %s", strings.Join(c.Aliases, ", "))
	}
	cc.Writeln("\r\n%s", c.Description)
	if c.Usage != "" {
		cc.Writeln("\r\n\033[1mUsage:\033[0m %s", c.Usage)
	}
	if c.FlagSetFunc != nil {
		fs := c.FlagSetFunc()
		hasFlags := false
		fs.VisitAll(func(f *flag.Flag) {
			if !hasFlags {
				cc.Writeln("\r\n\033[1mOptions:\033[0m")
				hasFlags = true
			}
		})
		if hasFlags {
			tabw := tabwriter.NewWriter(cc.Output, 0, 0, 1, ' ', 0)
			fs.VisitAll(func(f *flag.Flag) {
				fmt.Fprintf(tabw, "  \033[1m--%s\033[0m\t%s\t\r\n", f.Name, f.Usage)
			})
			tabw.Flush()
		}
	}
	if len(c.Examples) > 0 {
		cc.Writeln("\r\n\033[1mExamples:\033[0m")
		for _, ex := range c.Examples {
			cc.Writeln("  %s", ex)
		}
	}
	if len(c.Subcommands) > 0 {
		cc.Writeln("\r\n\033[1mSubcommands:\033[0m")
		tabw := tabwriter.NewWriter(cc.Output, 0, 0, 1, ' ', 0)
		for _, sub := range c.Subcommands {
			fmt.Fprintf(tabw, "  \033[1m%s\033[0m\t  - %s\t\r\n", sub.Name, sub.Description)
		}
		tabw.Flush()
	}
	return nil
}

// CommandContext provides all the context a command needs to execute
type CommandContext struct {
	// Core context
	User       *User
	Alloc      *Alloc
	PublicKey  string
	Args       []string
	FlagSet    *flag.FlagSet // parsed flags for this command
	SSHServer  *SSHServer
	SSHSession ssh.Session

	// I/O interfaces
	Output   io.Writer      // where to write output
	Terminal *term.Terminal // for interactive input (nil for non-interactive)
}

// Write is a convenience method for writing to the output
func (ctx *CommandContext) Write(format string, args ...interface{}) {
	fmt.Fprintf(ctx.Output, format, args...)
}

// Writeln writes a line with carriage return and newline
func (ctx *CommandContext) Writeln(format string, args ...interface{}) {
	fmt.Fprintf(ctx.Output, format+"\r\n", args...)
}

// ReadLine reads a line from the terminal (interactive mode only)
func (ctx *CommandContext) ReadLine() (string, error) {
	if ctx.Terminal == nil {
		return "", errors.New("not in interactive mode")
	}
	return ctx.Terminal.ReadLine()
}

// SetPrompt sets the terminal prompt (interactive mode only)
func (ctx *CommandContext) SetPrompt(prompt string) {
	if ctx.Terminal != nil {
		ctx.Terminal.SetPrompt(prompt)
	}
}

// IsInteractive returns true if this is an interactive session
func (ctx *CommandContext) IsInteractive() bool {
	return ctx.Terminal != nil
}

// CommandTree holds the root command and provides execution methods
type CommandTree struct {
	Commands []*Command
}

func (ct *CommandTree) Help(cc *CommandContext) {
	tabw := tabwriter.NewWriter(cc.Output, 1, 1, 0, ' ', 0)
	for _, cmd := range ct.GetAvailableCommands(cc) {
		nameStr := cmd.Name
		if len(cmd.Aliases) > 0 {
			nameStr = fmt.Sprintf("%s (%s)", cmd.Name, strings.Join(cmd.Aliases, ","))
		}
		fmt.Fprintf(tabw, "  \033[1m%s\033[0m\t  - %s\t\r\n", nameStr, cmd.Description)
	}
	tabw.Flush()
}

// FindCommand finds a command by path (e.g., ["billing", "update"] -> "the 'update' subcommand of 'billing'")
func (ct *CommandTree) FindCommand(path []string) *Command {
	if len(path) == 0 {
		return nil
	}

	for _, cmd := range ct.Commands {
		if cmd.Name == path[0] {
			return findCommandRecursive(cmd, path, 1)
		}
		for _, alias := range cmd.Aliases {
			if alias == path[0] {
				return findCommandRecursive(cmd, path, 1)
			}
		}
	}
	return nil
}

func findCommandRecursive(cmd *Command, path []string, depth int) *Command {
	if depth >= len(path) {
		return cmd
	}

	target := path[depth]

	// Look for exact name match or alias match
	for _, sub := range cmd.Subcommands {
		if sub.Name == target {
			return findCommandRecursive(sub, path, depth+1)
		}
		for _, alias := range sub.Aliases {
			if alias == target {
				return findCommandRecursive(sub, path, depth+1)
			}
		}
	}

	return nil
}

// ExecuteCommand executes a command with the given context and arguments
func (ct *CommandTree) ExecuteCommand(ctx context.Context, cc *CommandContext, commandPath []string) error {
	if len(commandPath) == 0 {
		return errors.New("no command specified")
	}

	// Find the deepest matching command
	var cmd *Command
	var remainingArgs []string

	// Treat the "help" command as a special case when it is the first thing in the command path.
	if commandPath[0] == "help" {
		cmd = ct.FindCommand([]string{"help"})
	} else {
		for i := len(commandPath); i > 0; i-- {
			cmd = ct.FindCommand(commandPath[:i])
			if cmd != nil && cmd.Handler != nil {
				remainingArgs = commandPath[i:]
				break
			}
		}
	}
	if cmd == nil || cmd.Handler == nil {
		// I think main doesn't return an error in this case.
		return fmt.Errorf("command not found: %s", strings.Join(commandPath, " "))
	}

	// Check if command is available
	if cmd.Available != nil && !cmd.Available(cc) {
		return fmt.Errorf("command not available: %s", strings.Join(commandPath, " "))
	}

	// Parse flags if the command has a FlagSetFunc
	// The remaining command path parts after finding the command are the actual arguments
	allArgs := remainingArgs
	// Parse the flags - always use a fresh FlagSet to avoid concurrent access
	var fs *flag.FlagSet
	if cmd.FlagSetFunc != nil {
		// Use the factory function to create a new FlagSet (thread-safe)
		fs = cmd.FlagSetFunc()
	} else {
		// Create a new empty FlagSet for commands without flags
		fs = flag.NewFlagSet("default", flag.ContinueOnError)
	}

	// Don't write flag help messages to the server's stdout/stderr, since we send them
	// to the user instead.
	fs.SetOutput(io.Discard)

	if err := fs.Parse(allArgs); err != nil {
		if err == flag.ErrHelp {
			return cmd.Help(cc)
		}
		return fmt.Errorf("flag parsing error: %v", err)
	}
	// Set the unparsed args as the new Args
	cc.Args = fs.Args()
	if cmd.FlagSetFunc != nil {
		// Set the FlagSet in context so handlers can access parsed flags
		cc.FlagSet = fs
	} else {
		// No custom flags, but we still used the default FlagSet for parsing
		cc.FlagSet = nil
	}
	if len(cc.Args) > 0 && !cmd.HasPositionalArgs {
		return fmt.Errorf("%q command not found, and %q command does not take positional arguments", strings.Join(cc.Args, " "), cmd.Name)
	}
	return cmd.Handler(ctx, cc)
}

// GetAvailableCommands returns commands available to the user
func (ct *CommandTree) GetAvailableCommands(ctx *CommandContext) []*Command {
	var available []*Command

	for _, cmd := range ct.Commands {
		if cmd.Available == nil || cmd.Available(ctx) {
			available = append(available, cmd)
		}
	}

	return available
}

// ANSIFilterWriter wraps an io.Writer and removes ANSI escape sequences
type ANSIFilterWriter struct {
	writer io.Writer
	ansiRe *regexp.Regexp
}

// NewANSIFilterWriter creates a new FilterWriter that removes ANSI control characters
func NewANSIFilterWriter(w io.Writer) *ANSIFilterWriter {
	// This regex matches ANSI escape sequences:
	// \x1b\[ matches the ESC[ sequence (or \033[)
	// [0-9;]*[a-zA-Z] matches parameters followed by a command letter
	// Also matches \x1b\([0-9;]*[a-zA-Z] for some other escape sequences
	ansiRegex := regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]|\x1b\([0-9;]*[a-zA-Z]|\x1b[=>]|\x1b[0-9;]*[a-zA-Z]`)

	return &ANSIFilterWriter{
		writer: w,
		ansiRe: ansiRegex,
	}
}

// Write implements io.Writer interface
func (fw *ANSIFilterWriter) Write(p []byte) (n int, err error) {
	// Remove ANSI escape sequences
	cleaned := fw.ansiRe.ReplaceAll(p, []byte{})

	// Write the cleaned data to the underlying writer
	_, err = fw.writer.Write(cleaned)
	if err != nil {
		return 0, err
	}

	// Return the number of bytes from the original input that were "written"
	// This maintains the io.Writer contract
	return len(p), nil
}
