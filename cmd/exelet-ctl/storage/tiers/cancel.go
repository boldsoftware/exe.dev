package tiers

import (
	"context"
	"fmt"

	"github.com/urfave/cli/v2"

	"exe.dev/cmd/exelet-ctl/helpers"
	api "exe.dev/pkg/api/exe/compute/v1"
)

var cancelCommand = &cli.Command{
	Name:      "cancel",
	Usage:     "Cancel a pending or in-progress tier migration",
	ArgsUsage: "<operation-id> [operation-id...]",
	Action: func(clix *cli.Context) error {
		if clix.NArg() < 1 {
			return fmt.Errorf("usage: storage tiers cancel <operation-id> [operation-id...]")
		}

		c, err := helpers.GetClient(clix)
		if err != nil {
			return err
		}
		defer c.Close()

		ctx := context.WithoutCancel(clix.Context)

		for _, opID := range clix.Args().Slice() {
			resp, err := c.CancelTierMigration(ctx, &api.CancelTierMigrationRequest{
				OperationID: opID,
			})
			if err != nil {
				fmt.Printf("error cancelling %s: %v\n", opID, err)
				continue
			}
			fmt.Printf("%s: %s\n", resp.OperationID, resp.State)
		}

		return nil
	},
}
