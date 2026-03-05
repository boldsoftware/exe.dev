package e1e

import (
	"testing"

	"exe.dev/e1e/testinfra"
)

func TestHelpCommandShowsNewOptions(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)

	pty, _, keyFile, _ := registerForExeDev(t)
	pty.SendLine("help new")
	pty.Want("Command: new")
	pty.Want("Options:")
	pty.Want("--name")
	pty.Want("--image")
	pty.WantPrompt()
	pty.Disconnect()

	pty = sshToExeDev(t, keyFile)
	pty.Reject(testinfra.Banner)
	pty.Reject("enter your email")
	pty.Reject("see a list of commands")
	pty.Want("create your first VM")
	pty.WantPrompt()
	pty.Disconnect()
}

// TestHelpIgnoresSubcommandFlags verifies that "help <cmd> --flag ..." doesn't
// fail with a flag parsing error. This was a bug where the help command's own
// FlagSet rejected flags that belong to the subcommand being queried.
func TestHelpIgnoresSubcommandFlags(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, _, _, _ := registerForExeDev(t)

	// "help integrations add" with subcommand flags should show help, not error.
	pty.Reject("flag parsing error")
	pty.SendLine("help integrations add --name=foo --target=https://example.com --header=X-Auth:secret")
	pty.Want("Command: add")
	pty.WantPrompt()

	// Bare "help integrations add" still works.
	pty.SendLine("help integrations add")
	pty.Want("Command: add")
	pty.WantPrompt()

	// "help <cmd>" with a single unknown flag should still show help.
	pty.SendLine("help whoami --verbose")
	pty.Want("Command: whoami")
	pty.WantPrompt()
}

// TestHelpJSON verifies that "help --json" still produces JSON output.
func TestHelpJSON(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, _, _, _ := registerForExeDev(t)

	// General help with --json should produce JSON array of commands.
	pty.SendLine("help --json")
	pty.Want(`"name"`)
	pty.Want(`"description"`)
	pty.WantPrompt()

	// Specific command help with --json should produce JSON too.
	pty.SendLine("help --json whoami")
	pty.Want(`"command"`)
	pty.Want(`"whoami"`)
	pty.WantPrompt()
}

// TestHelpGeneral verifies that bare "help" still lists all commands.
func TestHelpGeneral(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, _, _, _ := registerForExeDev(t)

	pty.SendLine("help")
	pty.Want("EXE.DEV")
	pty.Want("help <command>")
	pty.WantPrompt()
}
