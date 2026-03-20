package tiers

import (
	"context"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/urfave/cli/v2"

	"exe.dev/cmd/exelet-ctl/helpers"
	api "exe.dev/pkg/api/exe/compute/v1"
)

var statusCommand = &cli.Command{
	Name:  "status",
	Usage: "Show in-progress tier migration operations",
	Flags: []cli.Flag{
		&cli.BoolFlag{
			Name:  "clear",
			Usage: "clear completed and failed operations",
		},
	},
	Action: func(clix *cli.Context) error {
		c, err := helpers.GetClient(clix)
		if err != nil {
			return err
		}
		defer c.Close()

		ctx := context.WithoutCancel(clix.Context)

		if clix.Bool("clear") {
			resp, err := c.ClearTierMigrations(ctx, &api.ClearTierMigrationsRequest{})
			if err != nil {
				return fmt.Errorf("failed to clear tier migrations: %w", err)
			}
			fmt.Printf("Cleared %d completed/failed operations.\n", resp.Cleared)
			return nil
		}

		resp, err := c.GetTierMigrationStatus(ctx, &api.GetTierMigrationStatusRequest{})
		if err != nil {
			return fmt.Errorf("failed to get tier migration status: %w", err)
		}

		if len(resp.Operations) == 0 {
			fmt.Println("No tier migrations in progress.")
			return nil
		}

		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "OP ID\tINSTANCE\tFROM\tTO\tSTATE\tPROGRESS\tSTARTED")
		for _, op := range resp.Operations {
			elapsed := ""
			if op.StartedAt > 0 {
				elapsed = time.Since(time.Unix(op.StartedAt, 0)).Truncate(time.Second).String()
			}
			progress := fmt.Sprintf("%.0f%%", op.Progress*100)
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s ago\n",
				truncateID(op.OperationID), truncateID(op.InstanceID),
				op.SourcePool, op.TargetPool,
				op.State, progress, elapsed)
		}
		tw.Flush()

		return nil
	},
}

func truncateID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
