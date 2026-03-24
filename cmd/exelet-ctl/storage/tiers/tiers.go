package tiers

import (
	"github.com/urfave/cli/v2"
)

// Command is the storage tiers subcommand group
var Command = &cli.Command{
	Name:  "tiers",
	Usage: "Manage storage tiers",
	Subcommands: []*cli.Command{
		listCommand,
		migrateCommand,
		statusCommand,
		cancelCommand,
	},
}
