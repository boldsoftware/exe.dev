package execore

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/anmitsu/go-shlex"

	"exe.dev/execore/promptloop"
	"exe.dev/exemenu"
)

// promptCommand returns the hidden "prompt" command definition.
func (ss *SSHServer) promptCommand() *exemenu.Command {
	return &exemenu.Command{
		Name:              "prompt",
		Hidden:            true,
		Description:       "Interactive AI assistant for exe.dev",
		Usage:             "prompt <initial prompt>",
		Handler:           ss.handlePromptCommand,
		RawArgs:           true,
		HasPositionalArgs: true,
	}
}

func (ss *SSHServer) handlePromptCommand(ctx context.Context, cc *exemenu.CommandContext) error {
	if cc.SSHSession == nil {
		return cc.Errorf("prompt requires an interactive SSH session")
	}

	if len(cc.Args) < 1 {
		return cc.Errorf("usage: prompt <initial prompt>")
	}

	initialPrompt := strings.Join(cc.Args, " ")
	if strings.TrimSpace(initialPrompt) == "" {
		return cc.Errorf("prompt text is required")
	}

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

	systemPrompt := fmt.Sprintf(
		"You are an expert assistant for exe.dev, a cloud VM service. "+
			"You help the user manage their VMs via exe.dev commands. "+
			"Be concise and helpful. Use the exe_command tool to run read-only commands (ls, help, whoami, etc). "+
			"Use suggest_command for commands that modify state (new, rm, restart, cp, rename, resize). "+
			"The user's email is %s.",
		cc.User.Email,
	)

	return promptloop.Run(ctx, promptloop.Config{
		Model:        &promptloop.AnthropicModel{APIKey: apiKey, BaseURL: anthropicBaseURL},
		Dispatcher:   dispatcher,
		Output:       output,
		SystemPrompt: systemPrompt,
		ModelName:    shelleyDefaultModel,
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
	cc := &exemenu.CommandContext{
		User:      d.cc.User,
		PublicKey: d.cc.PublicKey,
		Output:    exemenu.NewANSIFilterWriter(&buf),
		ForceJSON: true,
		Logger:    d.cc.Logger,
	}

	exitCode := d.ss.commands.ExecuteCommand(ctx, cc, parts)
	return buf.String(), exitCode
}

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
