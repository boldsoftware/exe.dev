package execore

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/anmitsu/go-shlex"
	gliderssh "github.com/gliderlabs/ssh"

	"exe.dev/execore/promptloop"
	"exe.dev/exemenu"
)

// promptCommand returns the hidden "prompt" command definition.
func (ss *SSHServer) promptCommand() *exemenu.Command {
	return &exemenu.Command{
		Name:              "prompt",
		Hidden:            true,
		Description:       "Interactive AI assistant for exe.dev",
		Usage:             "prompt [initial prompt]",
		Handler:           ss.handlePromptCommand,
		RawArgs:           true,
		HasPositionalArgs: true,
	}
}

func (ss *SSHServer) handlePromptCommand(ctx context.Context, cc *exemenu.CommandContext) error {
	if cc.Terminal == nil {
		return cc.Errorf("prompt requires an interactive SSH session")
	}

	initialPrompt := strings.TrimSpace(strings.Join(cc.Args, " "))

	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		return cc.Errorf("ANTHROPIC_API_KEY not configured on server")
	}

	anthropicBaseURL := os.Getenv("ANTHROPIC_BASE_URL") // empty = default (api.anthropic.com)

	dispatcher := &commandTreeDispatcher{ss: ss, cc: cc}
	output := &terminalOutput{cc: cc}

	// Save and restore the lobby prompt since the prompt loop changes it.
	lobbyPrompt := fmt.Sprintf("\033[1;36m%s\033[0m \033[37m▶\033[0m ", ss.server.env.ReplHost)
	defer cc.SetPrompt(lobbyPrompt)

	cc.Writeln("\033[1;36m🤖 prompt\033[0m — interactive AI assistant")
	cc.Writeln("")

	// If no initial prompt provided, let the user type one interactively.
	if initialPrompt == "" {
		var err error
		initialPrompt, err = output.PromptUser("> ")
		if err != nil {
			return nil // ctrl+D → back to REPL
		}
		initialPrompt = strings.TrimSpace(initialPrompt)
		if initialPrompt == "" {
			return nil
		}
	}

	systemPrompt := fmt.Sprintf(
		"You are an expert assistant for exe.dev, a cloud VM service. "+
			"You help the user manage their VMs via exe.dev commands. "+
			"Be concise and helpful. Use the exe_command tool to run read-only commands (ls, help, whoami, etc). "+
			"Use suggest_command for commands that modify state (new, rm, restart, ssh, cp, rename, resize). "+
			"When suggest_command succeeds, the user already saw the command execute and its output — "+
			"just summarize the result or move on; do not say you \"suggested\" or \"recommended\" the command. "+
			"The user's email is %s.",
		cc.User.Email,
	)

	return promptloop.Run(ctx, promptloop.Config{
		Model:        &promptloop.AnthropicModel{APIKey: apiKey, BaseURL: anthropicBaseURL},
		Dispatcher:   dispatcher,
		Output:       output,
		SystemPrompt: systemPrompt,
	}, initialPrompt)
}

// commandTreeDispatcher dispatches commands through the exe.dev command tree.
type commandTreeDispatcher struct {
	ss *SSHServer
	cc *exemenu.CommandContext
}

func (d *commandTreeDispatcher) Dispatch(ctx context.Context, command string) (string, int) {
	parts, err := shlex.Split(command, true)
	if err != nil || len(parts) == 0 {
		return fmt.Sprintf("invalid command: %s", command), 1
	}

	// Create a new CommandContext that captures output to a buffer.
	var buf bytes.Buffer
	filtered := exemenu.NewANSIFilterWriter(&buf)
	cc := &exemenu.CommandContext{
		User:       d.cc.User,
		PublicKey:  d.cc.PublicKey,
		Output:     filtered,
		SSHSession: &bufferShellSession{buf: filtered, ctx: ctx},
		ForceJSON:  true,
		Logger:     d.cc.Logger,
	}

	exitCode := d.ss.commands.ExecuteCommand(ctx, cc, parts)
	return buf.String(), exitCode
}

// bufferShellSession is a minimal ShellSession implementation that captures
// output to a buffer. It's used by the prompt loop dispatcher so that commands
// like "ssh" that require an SSHSession can run in non-interactive exec mode.
type bufferShellSession struct {
	buf *exemenu.ANSIFilterWriter
	ctx context.Context
}

func (b *bufferShellSession) Read([]byte) (int, error)    { return 0, fmt.Errorf("not interactive") }
func (b *bufferShellSession) Write(p []byte) (int, error) { return b.buf.Write(p) }
func (b *bufferShellSession) Close() error                { return nil }
func (b *bufferShellSession) Push([]byte)                 {}
func (b *bufferShellSession) Context() context.Context    { return b.ctx }
func (b *bufferShellSession) Environ() []string           { return nil }
func (b *bufferShellSession) User() string                { return "" }
func (b *bufferShellSession) Pty() (gliderssh.Pty, bool)  { return gliderssh.Pty{}, false }
func (b *bufferShellSession) WaitWindowChange() bool      { return false }

// terminalOutput adapts the exemenu terminal for promptloop.Output.
type terminalOutput struct {
	cc *exemenu.CommandContext
}

func (o *terminalOutput) WriteText(text string) {
	text = strings.ReplaceAll(text, "\n", "\r\n")
	o.cc.Write("\033[0m%s\r\n", text)
}

func (o *terminalOutput) WriteToolCall(name, input string) {
	o.cc.Write("\033[2m⚡ %s: %s\033[0m\r\n", name, input)
}

func (o *terminalOutput) WriteToolResult(name, result string, isError bool) {
	if isError {
		result = strings.ReplaceAll(result, "\n", "\r\n")
		o.cc.Write("\033[31m✗ %s: %s\033[0m\r\n", name, truncateForTerminal(result, 500))
	} else {
		lines := strings.Count(result, "\n")
		if lines > 5 {
			o.cc.Write("\033[2m  (%d lines)\033[0m\r\n", lines)
		}
	}
}

func (o *terminalOutput) PromptUser(prompt string) (string, error) {
	if o.cc.Terminal == nil {
		return "", fmt.Errorf("not interactive")
	}
	prompt = strings.ReplaceAll(prompt, "\n", "\r\n")
	o.cc.SetPrompt(prompt)
	return o.cc.ReadLine()
}

func (o *terminalOutput) WriteStatus(text string) {
	o.cc.Write("\033[2m%s\033[0m\r\n", text)
}

func truncateForTerminal(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
