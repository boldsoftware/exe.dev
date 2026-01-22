package replication

import (
	"github.com/urfave/cli/v2"
)

// Command is the replication subcommand
var Command = &cli.Command{
	Name:  "replication",
	Usage: "Manage storage replication",
	Subcommands: []*cli.Command{
		statusCommand,
		runCommand,
		historyCommand,
		listCommand,
		restoreCommand,
	},
}
