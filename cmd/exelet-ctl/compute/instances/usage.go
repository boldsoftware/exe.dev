package instances

import (
	"context"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/dustin/go-humanize"
	"github.com/urfave/cli/v2"

	"exe.dev/cmd/exelet-ctl/helpers"
	resourceapi "exe.dev/pkg/api/exe/resource/v1"
)

var usageInstanceCommand = &cli.Command{
	Name:      "usage",
	Usage:     "Get resource usage for compute instances",
	ArgsUsage: "<id> [id...]",
	Flags: []cli.Flag{
		&cli.BoolFlag{
			Name:  "fs",
			Usage: "Ask the exelet to read the ext4 superblock and report guest filesystem usage (subject to the exelet's gate)",
		},
	},
	Action: func(clix *cli.Context) error {
		if clix.NArg() < 1 {
			return fmt.Errorf("at least one instance ID required")
		}

		c, err := helpers.GetClient(clix)
		if err != nil {
			return err
		}
		defer c.Close()

		ctx := context.WithoutCancel(clix.Context)
		ids := clix.Args().Slice()
		collectFs := clix.Bool("fs")

		w := tabwriter.NewWriter(os.Stdout, 12, 1, 3, ' ', 0)
		if collectFs {
			fmt.Fprintf(w, "ID\tNAME\tCPU %%\tCPU TIME\tMEMORY\tDISK\tFS USED/CAP\tNET RX\tNET TX\tPRIORITY\n")
		} else {
			fmt.Fprintf(w, "ID\tNAME\tCPU %%\tCPU TIME\tMEMORY\tDISK\tNET RX\tNET TX\tPRIORITY\n")
		}

		for _, id := range ids {
			resp, err := c.GetVMUsage(ctx, &resourceapi.GetVMUsageRequest{
				VmID:                   id,
				CollectFilesystemUsage: collectFs,
			})
			if err != nil {
				fmt.Fprintf(os.Stderr, "error getting usage for %s: %v\n", id, err)
				continue
			}

			u := resp.Usage
			if collectFs {
				fsCol := "-"
				if u.FsTotalBytes > 0 {
					fsCol = fmt.Sprintf("%s/%s", humanize.Bytes(u.FsTotalBytes-u.FsAvailableBytes), humanize.Bytes(u.FsTotalBytes))
				}
				fmt.Fprintf(w, "%s\t%s\t%.1f%%\t%.2fs\t%s\t%s\t%s\t%s\t%s\t%s\n",
					u.ID, u.Name, u.CpuPercent, u.CpuSeconds,
					humanize.Bytes(u.MemoryBytes), humanize.Bytes(u.DiskBytes),
					fsCol,
					humanize.Bytes(u.NetRxBytes), humanize.Bytes(u.NetTxBytes),
					formatPriority(u.Priority),
				)
			} else {
				fmt.Fprintf(w, "%s\t%s\t%.1f%%\t%.2fs\t%s\t%s\t%s\t%s\t%s\n",
					u.ID, u.Name, u.CpuPercent, u.CpuSeconds,
					humanize.Bytes(u.MemoryBytes), humanize.Bytes(u.DiskBytes),
					humanize.Bytes(u.NetRxBytes), humanize.Bytes(u.NetTxBytes),
					formatPriority(u.Priority),
				)
			}
		}

		return w.Flush()
	},
}

func formatPriority(p resourceapi.VMPriority) string {
	switch p {
	case resourceapi.VMPriority_PRIORITY_NORMAL:
		return "normal"
	case resourceapi.VMPriority_PRIORITY_LOW:
		return "low"
	case resourceapi.VMPriority_PRIORITY_AUTO:
		return "auto"
	default:
		return "-"
	}
}
