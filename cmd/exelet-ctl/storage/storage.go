package storage

import (
	"github.com/urfave/cli/v2"

	"exe.dev/cmd/exelet-ctl/storage/filesystems"
)

var Command = &cli.Command{
	Name:  "storage",
	Usage: "Manage Storage Resources",
	Subcommands: []*cli.Command{
		filesystems.Command,
	},
}
