package resources

import (
	"github.com/urfave/cli/v2"
)

var Command = &cli.Command{
	Name:  "resources",
	Usage: "Manage Resources",
	Subcommands: []*cli.Command{
		machineCommand,
	},
}
