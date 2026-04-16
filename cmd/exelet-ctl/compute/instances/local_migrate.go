package instances

import (
	"context"
	"fmt"

	"github.com/urfave/cli/v2"

	"exe.dev/cmd/exelet-ctl/helpers"
	api "exe.dev/pkg/api/exe/compute/v1"
)

var localMigrateInstanceCommand = &cli.Command{
	Name:      "local-migrate",
	Usage:     "perform in-place local live migration of compute instance(s)",
	ArgsUsage: "[ID ...]",
	Action: func(clix *cli.Context) error {
		c, err := helpers.GetClient(clix)
		if err != nil {
			return err
		}
		defer c.Close()

		ctx := context.WithoutCancel(clix.Context)
		ids := clix.Args().Slice()
		total := len(ids)
		var ok, cold, failed int

		for i, id := range ids {
			fmt.Printf("[%d/%d] migrating %s...", i+1, total, id)

			resp, err := c.LiveMigrateLocal(ctx, &api.LiveMigrateLocalRequest{
				InstanceID: id,
			})
			if err != nil {
				failed++
				fmt.Printf(" FAILED: %s\n", err)
				continue
			}

			switch resp.Outcome {
			case api.LiveMigrateLocalResponse_COLD_RESTARTED:
				cold++
				fmt.Printf(" cold restarted in %dms (%s)\n", resp.DowntimeMs, resp.MigrationError)
			default:
				ok++
				fmt.Printf(" ok (%dms)\n", resp.DowntimeMs)
			}
		}

		fmt.Printf("\ndone: %d ok, %d cold restarted, %d failed (out of %d)\n", ok, cold, failed, total)
		return nil
	},
}
