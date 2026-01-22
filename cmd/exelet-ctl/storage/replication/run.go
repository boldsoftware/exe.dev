package replication

import (
	"context"
	"fmt"

	"github.com/urfave/cli/v2"

	"exe.dev/cmd/exelet-ctl/helpers"
	api "exe.dev/pkg/api/exe/replication/v1"
)

var runCommand = &cli.Command{
	Name:      "run",
	Usage:     "Trigger immediate replication",
	ArgsUsage: "[volume-id]",
	Description: `Trigger an immediate replication cycle.

If no volume ID is specified, triggers replication for all volumes.
If a volume ID is specified, only that volume will be replicated.`,
	Action: func(clix *cli.Context) error {
		c, err := helpers.GetClient(clix)
		if err != nil {
			return err
		}
		defer c.Close()

		ctx := context.WithoutCancel(clix.Context)

		volumeID := ""
		if clix.NArg() > 0 {
			volumeID = clix.Args().First()
		}

		resp, err := c.TriggerReplication(ctx, &api.TriggerReplicationRequest{
			VolumeID: volumeID,
		})
		if err != nil {
			return fmt.Errorf("failed to trigger replication: %w", err)
		}

		if volumeID != "" {
			fmt.Printf("Queued replication for volume: %s\n", volumeID)
		} else if resp.QueuedCount == -1 {
			fmt.Println("Triggered full replication cycle")
		} else {
			fmt.Printf("Queued %d volumes for replication\n", resp.QueuedCount)
		}

		return nil
	},
}
