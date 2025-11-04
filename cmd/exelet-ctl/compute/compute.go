package compute

import (
	"github.com/urfave/cli/v2"

	"exe.dev/cmd/exelet-ctl/compute/instances"
)

var Command = &cli.Command{
	Name:  "compute",
	Usage: "Manage Compute Resources",
	Subcommands: []*cli.Command{
		instances.Command,
	},
}
