package exe

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"text/tabwriter"

	"github.com/anmitsu/go-shlex"
	"github.com/charmbracelet/x/ansi"
	"github.com/gliderlabs/ssh"
	"github.com/google/uuid"
	"golang.org/x/term"
)

// CompletionContext provides context for tab completion
type CompletionContext struct {
	Line        string   // current input line
	Cursor      int      // cursor position in line
	Words       []string // parsed words so far
	CurrentWord string   // word being completed (might be partial)
	Position    int      // which argument position we're completing (0 = command, 1 = first arg, etc.)
}

// Command represents a single command in the command tree
type Command struct {
	Name              string
	Hidden            bool // if true, command is hidden from help and completions
	Aliases           []string
	Description       string
	Usage             string
	FlagSetFunc       func() *flag.FlagSet // Factory to create a new FlagSet for each invocation
	Examples          []string
	Subcommands       []*Command
	HasPositionalArgs bool
	Handler           func(context.Context, *CommandContext) error
	Available         func(ctx *CommandContext) bool                     // nil means always available
	CompleterFunc     func(*CompletionContext, *CommandContext) []string // Custom completion for command arguments
}

func (ct *CommandTree) SubcommandNames(c *Command) []string {
	var names []string
	for _, sc := range c.Subcommands {
		if sc.Hidden && !ct.DevMode {
			continue
		}
		names = append(names, sc.Name)
	}
	return names
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
			if sub.Hidden {
				continue
			}
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
	DevMode    bool // if true, show hidden commands in help and completions

	// I/O interfaces
	Output   io.Writer      // where to write output
	Terminal *term.Terminal // for interactive input (nil for non-interactive)
}

// Write is a convenience method for writing to the output.
// It is a no-op when cc.WantJSON() reports true.
func (ctx *CommandContext) Write(format string, args ...any) {
	if ctx.WantJSON() {
		return
	}
	fmt.Fprintf(ctx.Output, format, args...)
}

// A CommandClientError is an error returned by a command handler to indicate
// bad input from the user/client.
type CommandClientError struct {
	Err error
}

func (cce CommandClientError) Error() string {
	return cce.Err.Error()
}

func (cce CommandClientError) Unwrap() error {
	return cce.Err
}

// Errof returns an CommandClientError with a formatted message.
func (ctx *CommandContext) Errorf(msg string, args ...any) error {
	return CommandClientError{Err: fmt.Errorf(msg, args...)}
}

// WriteJSON is a convenience method for json output.
func (ctx *CommandContext) WriteJSON(x any) {
	data, err := json.Marshal(x)
	if err != nil {
		fmt.Fprintf(ctx.Output, "failed to marshal JSON: %v\r\n", err)
		return
	}
	fmt.Fprintf(ctx.Output, "%s\n", data)
}

func (ctx *CommandContext) WriteInternalError(cmd string, err error, slogDetails ...any) {
	if ctx.DevMode {
		ctx.Write("\033[1;31mRaw error (dev only):\r\n%v\033[0m\r\n\r\n", err)
	}
	guid := uuid.New().String() // for x-ref on support tickets
	attrs := []any{
		"error", err,
		"command_context", ctx,
		"guid", guid,
	}
	attrs = append(attrs, slogDetails...)
	slog.Error("ssh command failed unexpectedly", attrs...)
	ctx.WriteError("%q: internal error, error ID: %s", cmd, guid)
}

// WantJSON reports whether the --json flag is set.
func (ctx *CommandContext) WantJSON() bool {
	if ctx.FlagSet == nil {
		return false
	}
	flag := ctx.FlagSet.Lookup("json")
	return flag != nil && flag.Value.String() == "true"
}

// WriteError outputs an error message in either JSON or formatted text
func (ctx *CommandContext) WriteError(message string, args ...any) {
	if ctx.WantJSON() {
		errorOutput := map[string]string{
			"error": fmt.Sprintf(message, args...),
		}
		ctx.WriteJSON(errorOutput)
		return
	}
	ctx.Write("\033[1;31m"+message+"\033[0m\r\n", args...)
}

// Writeln writes a line with carriage return and newline
func (ctx *CommandContext) Writeln(format string, args ...any) {
	if ctx.WantJSON() {
		return
	}
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

// ValidateCommand validates a command's configuration to catch common mistakes
func ValidateCommand(cmd *Command) error {
	if cmd.HasPositionalArgs && len(cmd.Subcommands) > 0 {
		return fmt.Errorf("command %q cannot have both positional arguments and subcommands", cmd.Name)
	}

	// Recursively validate subcommands
	for _, subCmd := range cmd.Subcommands {
		if err := ValidateCommand(subCmd); err != nil {
			return fmt.Errorf("in subcommand of %q: %w", cmd.Name, err)
		}
	}

	return nil
}

// CommandTree holds the root command and provides execution methods
type CommandTree struct {
	Commands []*Command
	DevMode  bool // if true, show hidden commands in help and completions
}

func (ct *CommandTree) Help(cc *CommandContext) {
	tabw := tabwriter.NewWriter(cc.Output, 1, 1, 0, ' ', 0)
	for _, cmd := range ct.GetAvailableCommands(cc) {
		if cmd.Hidden && !ct.DevMode {
			continue
		}
		nameStr := cmd.Name
		if len(cmd.Aliases) > 0 {
			nameStr = fmt.Sprintf("%s (%s)", cmd.Name, strings.Join(cmd.Aliases, ","))
		}
		var hidden string
		if cmd.Hidden {
			hidden = " [hidden]"
		}
		fmt.Fprintf(tabw, "  \033[1m%s\033[0m\t  - %s%s\t\r\n", nameStr, cmd.Description, hidden)
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

// ExecuteCommand executes a command with the given context and arguments.
// It returns an exit code: 0 for success, >0 for failure, -1 for EOF.
func (ct *CommandTree) ExecuteCommand(ctx context.Context, cc *CommandContext, commandPath []string) int {
	err := ct.executeCommand(ctx, cc, commandPath)
	if errors.Is(err, io.EOF) {
		return -1
	}
	if err == nil {
		return 0
	}
	var cce CommandClientError
	if ok := errors.As(err, &cce); ok {
		cc.WriteError("%v", err)
	} else {
		cc.WriteInternalError(strings.Join(commandPath, " "), err)
	}
	return 1
}

func (ct *CommandTree) executeCommand(ctx context.Context, cc *CommandContext, commandPath []string) (err error) {
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

	// Allow flags to be interspersed with positional args by moving flags first
	if len(allArgs) > 0 {
		var flagArgs []string
		var posArgs []string
		for i := 0; i < len(allArgs); i++ {
			a := allArgs[i]
			// Terminator: everything after -- is positional
			if a == "--" {
				posArgs = append(posArgs, allArgs[i+1:]...)
				break
			}
			// Treat args starting with '-' as flags
			if strings.HasPrefix(a, "-") {
				flagArgs = append(flagArgs, a)
				// If next token looks like a value (doesn't start with '-') and current
				// flag didn't include '=', treat it as the value for this flag.
				if !strings.Contains(a, "=") && i+1 < len(allArgs) && !strings.HasPrefix(allArgs[i+1], "-") {
					flagArgs = append(flagArgs, allArgs[i+1])
					i++
				}
				continue
			}
			// Positional argument
			posArgs = append(posArgs, a)
		}
		// Recombine: flags first, then positionals
		combined := make([]string, 0, len(flagArgs)+len(posArgs))
		combined = append(combined, flagArgs...)
		combined = append(combined, posArgs...)
		allArgs = combined
	}

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
		if len(cmd.Subcommands) > 0 {
			return fmt.Errorf(`%q subcommand %q not found, valid %q subcommands are: %s`, commandPath[0], cc.Args[0], commandPath[0], strings.Join(ct.SubcommandNames(cmd), ", "))
		}
		return fmt.Errorf("%q command has no subcommands and does not take positional arguments", cmd.Name)
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
}

// NewANSIFilterWriter creates a new FilterWriter that removes ANSI control characters
func NewANSIFilterWriter(w io.Writer) *ANSIFilterWriter {
	return &ANSIFilterWriter{writer: w}
}

// Write implements io.Writer interface
func (fw *ANSIFilterWriter) Write(p []byte) (n int, err error) {
	cleaned := []byte(ansi.Strip(string(p)))
	_, err = fw.writer.Write(cleaned)
	if err != nil {
		return 0, err
	}
	// Pretend we wrote all the original bytes.
	return len(p), nil
}

// CompleteCommand provides tab completion for a given input line
func (ct *CommandTree) CompleteCommand(line string, cursor int, cc *CommandContext) []string {
	// Parse the line up to cursor position
	lineUpToCursor := line[:cursor]
	words, currentWord, position := parseCompletionInput(lineUpToCursor)

	compCtx := &CompletionContext{
		Line:        line,
		Cursor:      cursor,
		Words:       words,
		CurrentWord: currentWord,
		Position:    position,
	}

	if position == 0 {
		// Completing command name
		return ct.completeCommandName(compCtx, cc)
	}

	// Find the command being executed
	cmd := ct.FindCommand(words[:1])
	if cmd == nil {
		return nil
	}

	// Check if we're completing subcommands
	if len(cmd.Subcommands) > 0 {
		// Calculate how many args we have after the main command
		argsAfterCommand := len(words) - 1

		// Try to find if we've already selected a subcommand
		if argsAfterCommand > 0 {
			// Use the shared function to find the deepest subcommand
			deepestCmd, remainingArgs := ct.findDeepestSubcommand(words)
			if deepestCmd != nil && len(remainingArgs) == 0 {
				cmd = deepestCmd
			} else {
				// We're still in the process of completing subcommand names
				return ct.completeSubcommand(cmd, compCtx, cc)
			}
		} else {
			// First argument after command - complete subcommand names
			return ct.completeSubcommand(cmd, compCtx, cc)
		}
	}

	// Check if we're completing a flag
	if strings.HasPrefix(currentWord, "-") {
		return ct.completeFlag(cmd, compCtx, cc)
	}

	// Use custom completer if available
	if cmd.CompleterFunc != nil {
		return cmd.CompleterFunc(compCtx, cc)
	}

	// Default: no completions
	return nil
}

// parseCompletionInput parses input line for completion context
func parseCompletionInput(line string) (words []string, currentWord string, position int) {
	if strings.TrimSpace(line) == "" {
		return []string{}, "", 0
	}

	// Use shlex to parse, but handle incomplete input
	words, err := shlex.Split(line, true)
	if err != nil {
		// If parsing fails, likely due to incomplete quotes, try without quotes
		words = strings.Fields(line)
	}

	if len(words) == 0 {
		return []string{}, "", 0
	}

	// Check if line ends with whitespace (starting new word) or not (completing current word)
	if strings.HasSuffix(line, " ") || strings.HasSuffix(line, "\t") {
		// Starting a new word
		return words, "", len(words)
	}

	// Completing current word
	currentWord = words[len(words)-1]
	words = words[:len(words)-1]
	return words, currentWord, len(words)
}

// completeCommandName completes top-level command names
func (ct *CommandTree) completeCommandName(compCtx *CompletionContext, cc *CommandContext) []string {
	var completions []string
	prefix := compCtx.CurrentWord

	for _, cmd := range ct.GetAvailableCommands(cc) {
		if cmd.Hidden && !ct.DevMode {
			continue
		}
		if strings.HasPrefix(cmd.Name, prefix) {
			completions = append(completions, cmd.Name)
		}
		// Also check aliases
		for _, alias := range cmd.Aliases {
			if strings.HasPrefix(alias, prefix) {
				completions = append(completions, alias)
			}
		}
	}

	return completions
}

// completeSubcommand completes subcommand names
func (ct *CommandTree) completeSubcommand(cmd *Command, compCtx *CompletionContext, cc *CommandContext) []string {
	var completions []string
	prefix := compCtx.CurrentWord

	for _, subCmd := range cmd.Subcommands {
		if subCmd.Hidden && !ct.DevMode {
			continue
		}
		if subCmd.Available == nil || subCmd.Available(cc) {
			if strings.HasPrefix(subCmd.Name, prefix) {
				completions = append(completions, subCmd.Name)
			}
			// Also check aliases
			for _, alias := range subCmd.Aliases {
				if strings.HasPrefix(alias, prefix) {
					completions = append(completions, alias)
				}
			}
		}
	}

	return completions
}

// completeFlag completes flag names
func (ct *CommandTree) completeFlag(cmd *Command, compCtx *CompletionContext, cc *CommandContext) []string {
	if cmd.FlagSetFunc == nil {
		return nil
	}

	var completions []string
	prefix := compCtx.CurrentWord
	flagSet := cmd.FlagSetFunc()

	flagSet.VisitAll(func(f *flag.Flag) {
		longFlag := "--" + f.Name
		if strings.HasPrefix(longFlag, prefix) {
			completions = append(completions, longFlag)
		}
		// Add short flag if prefix is short enough
		if len(prefix) <= 2 && len(f.Name) > 0 {
			shortFlag := "-" + string(f.Name[0])
			if strings.HasPrefix(shortFlag, prefix) {
				completions = append(completions, shortFlag)
			}
		}
	})

	return completions
}

// findDeepestSubcommand finds the deepest matching subcommand given a path
// Returns the command and the remaining unconsumed path segments
func (ct *CommandTree) findDeepestSubcommand(commandPath []string) (*Command, []string) {
	if len(commandPath) == 0 {
		return nil, commandPath
	}

	// Start with the root command
	cmd := ct.FindCommand(commandPath[:1])
	if cmd == nil {
		return nil, commandPath
	}

	// Walk down the subcommand tree as far as we can go
	current := cmd
	consumed := 1

	for consumed < len(commandPath) && len(current.Subcommands) > 0 {
		nextSegment := commandPath[consumed]
		found := false

		for _, subCmd := range current.Subcommands {
			if subCmd.Name == nextSegment {
				current = subCmd
				consumed++
				found = true
				break
			}
			// Check aliases
			for _, alias := range subCmd.Aliases {
				if alias == nextSegment {
					current = subCmd
					consumed++
					found = true
					break
				}
			}
			if found {
				break
			}
		}

		if !found {
			break
		}
	}

	return current, commandPath[consumed:]
}

// Common completion functions

// CompleteBoxNames provides completion for box names
func CompleteBoxNames(compCtx *CompletionContext, cc *CommandContext) []string {
	if cc.SSHServer == nil || cc.SSHServer.server == nil || cc.SSHServer.server.containerManager == nil {
		return nil
	}

	// Get user's containers
	ctx := context.Background() // Use a background context for completion
	containers, err := cc.SSHServer.server.containerManager.ListContainers(ctx, cc.Alloc.AllocID)
	if err != nil {
		return nil
	}

	var completions []string
	prefix := compCtx.CurrentWord

	for _, container := range containers {
		if strings.HasPrefix(container.Name, prefix) {
			completions = append(completions, container.Name)
		}
	}

	return completions
}
