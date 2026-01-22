package replication

import (
	"context"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/urfave/cli/v2"

	"exe.dev/cmd/exelet-ctl/helpers"
	api "exe.dev/pkg/api/exe/replication/v1"
)

var listCommand = &cli.Command{
	Name:      "list",
	Usage:     "List snapshots for a volume",
	ArgsUsage: "<volume-id>",
	Description: `List available snapshots for a volume on the remote target.

This is useful to see what snapshots are available for restore.

Examples:
  exelet-ctl storage replication list vm000123-box-myvm`,
	Flags: []cli.Flag{
		&cli.BoolFlag{
			Name:    "local",
			Aliases: []string{"l"},
			Usage:   "also show local snapshots",
		},
	},
	Action: func(clix *cli.Context) error {
		if clix.NArg() < 1 {
			return fmt.Errorf("usage: exelet-ctl storage replication list <volume-id>")
		}

		volumeID := clix.Args().Get(0)
		showLocal := clix.Bool("local")

		c, err := helpers.GetClient(clix)
		if err != nil {
			return err
		}
		defer c.Close()

		ctx := context.WithoutCancel(clix.Context)
		resp, err := c.ListSnapshots(ctx, &api.ListSnapshotsRequest{
			VolumeID: volumeID,
		})
		if err != nil {
			return fmt.Errorf("failed to list snapshots: %w", err)
		}

		printSnapshots("Remote Snapshots", resp.RemoteSnapshots)

		if showLocal {
			fmt.Println()
			printSnapshots("Local Snapshots", resp.LocalSnapshots)
		}

		return nil
	},
}

func printSnapshots(header string, snapshots []*api.SnapshotInfo) {
	fmt.Printf("%s:\n", header)

	if len(snapshots) == 0 {
		fmt.Println("  (none)")
		return
	}

	w := tabwriter.NewWriter(os.Stdout, 2, 1, 3, ' ', 0)
	fmt.Fprintf(w, "  NAME\tCREATED\tSIZE\n")

	for _, snap := range snapshots {
		created := "-"
		if snap.CreatedAt > 0 {
			created = time.Unix(snap.CreatedAt, 0).Format("2006-01-02 15:04:05")
		}

		size := "-"
		if snap.SizeBytes > 0 {
			size = formatBytes(snap.SizeBytes)
		}

		fmt.Fprintf(w, "  %s\t%s\t%s\n", snap.Name, created, size)
	}

	w.Flush()
}
