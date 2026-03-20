package tiers

import (
	"context"
	"fmt"

	"github.com/urfave/cli/v2"

	"exe.dev/cmd/exelet-ctl/helpers"
	api "exe.dev/pkg/api/exe/compute/v1"
)

var migrateCommand = &cli.Command{
	Name:      "migrate",
	Usage:     "Migrate instances between storage tiers",
	ArgsUsage: "<target-pool> <instance-id> [instance-id...]",
	Flags: []cli.Flag{
		&cli.BoolFlag{
			Name:  "live",
			Usage: "enable live migration (near-zero downtime for running VMs)",
		},
	},
	Action: func(clix *cli.Context) error {
		if clix.NArg() < 2 {
			return fmt.Errorf("usage: exelet-ctl storage tiers migrate <target-pool> <instance-id> [instance-id...]")
		}

		targetPool := clix.Args().Get(0)
		instanceIDs := clix.Args().Slice()[1:]
		live := clix.Bool("live")

		c, err := helpers.GetClient(clix)
		if err != nil {
			return err
		}
		defer c.Close()

		ctx := context.WithoutCancel(clix.Context)

		for _, id := range instanceIDs {
			resp, err := c.MigrateStorageTier(ctx, &api.MigrateStorageTierRequest{
				InstanceID: id,
				TargetPool: targetPool,
				Live:       live,
			})
			if err != nil {
				fmt.Printf("ERROR  %s: %v\n", id, err)
				continue
			}
			fmt.Printf("QUEUED %s: %s -> %s (op: %s)\n", resp.InstanceID, resp.SourcePool, resp.TargetPool, resp.OperationID)
		}

		return nil
	},
}
