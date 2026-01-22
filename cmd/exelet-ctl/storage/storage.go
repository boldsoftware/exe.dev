package storage

import (
	"github.com/urfave/cli/v2"

	"exe.dev/cmd/exelet-ctl/storage/filesystems"
	"exe.dev/cmd/exelet-ctl/storage/replication"
)

var Command = &cli.Command{
	Name:  "storage",
	Usage: "Manage Storage Resources",
	Subcommands: []*cli.Command{
		filesystems.Command,
		replication.Command,
	},
}
