package replication

import (
	"context"
	"fmt"

	"github.com/urfave/cli/v2"

	"exe.dev/cmd/exelet-ctl/helpers"
	api "exe.dev/pkg/api/exe/replication/v1"
)

var restoreCommand = &cli.Command{
	Name:      "restore",
	Usage:     "Restore a volume from backup",
	ArgsUsage: "<volume-id> <snapshot-name>",
	Description: `Restore a volume from a backup.

For SSH targets, snapshot-name is the replication snapshot (e.g., repl-20240115T143022Z).
For file targets, snapshot-name is the backup filename.

Examples:
  exelet-ctl storage replication restore vm000123-box-myvm repl-20240115T143022Z
  exelet-ctl storage replication restore --force vm000123-box-myvm repl-20240115T143022Z`,
	Flags: []cli.Flag{
		&cli.BoolFlag{
			Name:    "force",
			Aliases: []string{"f"},
			Usage:   "overwrite existing volume if present",
		},
	},
	Action: func(clix *cli.Context) error {
		if clix.NArg() < 2 {
			return fmt.Errorf("usage: exelet-ctl storage replication restore [--force] <volume-id> <snapshot-name>")
		}

		volumeID := clix.Args().Get(0)
		targetRef := clix.Args().Get(1)
		force := clix.Bool("force")

		c, err := helpers.GetClient(clix)
		if err != nil {
			return err
		}
		defer c.Close()

		ctx := context.WithoutCancel(clix.Context)

		s := helpers.NewSpinner("restoring volume")
		s.Start()

		resp, err := c.RestoreVolume(ctx, &api.RestoreVolumeRequest{
			TargetRef: targetRef,
			VolumeID:  volumeID,
			Force:     force,
		})
		s.Stop()

		if err != nil {
			return fmt.Errorf("restore failed: %w", err)
		}

		fmt.Printf("Restored volume: %s\n", resp.VolumeID)
		if resp.BytesRestored > 0 {
			fmt.Printf("Bytes restored: %s\n", formatBytes(resp.BytesRestored))
		}

		return nil
	},
}
