package replication

import (
	"context"
	"fmt"
	"io"
	"os"
	"text/tabwriter"
	"time"

	"github.com/urfave/cli/v2"

	"exe.dev/cmd/exelet-ctl/helpers"
	api "exe.dev/pkg/api/exe/replication/v1"
)

var historyCommand = &cli.Command{
	Name:  "history",
	Usage: "Show replication snapshots on remote target",
	Flags: []cli.Flag{
		&cli.IntFlag{
			Name:    "limit",
			Aliases: []string{"n"},
			Usage:   "maximum number of entries to show",
			Value:   20,
		},
	},
	Action: func(clix *cli.Context) error {
		c, err := helpers.GetClient(clix)
		if err != nil {
			return err
		}
		defer c.Close()

		ctx := context.WithoutCancel(clix.Context)

		limit := clix.Int("limit")
		stream, err := c.ListRemoteSnapshots(ctx, &api.ListRemoteSnapshotsRequest{
			Limit: int32(limit),
		})
		if err != nil {
			return fmt.Errorf("failed to list remote snapshots: %w", err)
		}

		w := tabwriter.NewWriter(os.Stdout, 2, 1, 3, ' ', 0)
		fmt.Fprintf(w, "DATE\tVOLUME ID\tSNAPSHOT\tSIZE\n")

		for {
			resp, err := stream.Recv()
			if err == io.EOF {
				break
			}
			if err != nil {
				return fmt.Errorf("error receiving snapshots: %w", err)
			}

			if resp.Snapshot == nil {
				continue
			}

			date := "-"
			if resp.Snapshot.CreatedAt > 0 {
				date = time.Unix(resp.Snapshot.CreatedAt, 0).Format("2006-01-02 15:04")
			}
			size := formatBytes(resp.Snapshot.SizeBytes)

			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
				date,
				resp.VolumeID,
				resp.Snapshot.Name,
				size,
			)
		}

		w.Flush()
		return nil
	},
}
