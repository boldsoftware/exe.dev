package instances

import "github.com/urfave/cli/v2"

var Command = &cli.Command{
	Name:  "instances",
	Usage: "Manage Compute Instances",
	Subcommands: []*cli.Command{
		createInstanceCommand,
		listInstancesCommand,
		getInstanceCommand,
		usageInstanceCommand,
		instanceLogsCommand,
		startInstanceCommand,
		stopInstanceCommand,
		updateInstanceCommand,
		setGroupInstanceCommand,
		deleteInstanceCommand,
		migrateInstanceCommand,
	},
}
