package filesystems

import "github.com/urfave/cli/v2"

var Command = &cli.Command{
	Name:    "filesystems",
	Aliases: []string{"fs"},
	Usage:   "Manage Filesystems",
	Subcommands: []*cli.Command{
		getFilesystemCommand,
		loadFilesystemCommand,
	},
}
