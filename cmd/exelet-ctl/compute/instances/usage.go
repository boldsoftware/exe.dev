package instances

import (
	"context"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/dustin/go-humanize"
	"github.com/urfave/cli/v2"

	"exe.dev/cmd/exelet-ctl/helpers"
	resourceapi "exe.dev/pkg/api/exe/resource/v1"
)

var usageInstanceCommand = &cli.Command{
	Name:      "usage",
	Usage:     "Get resource usage for compute instances",
	ArgsUsage: "<id> [id...]",
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

		w := tabwriter.NewWriter(os.Stdout, 12, 1, 3, ' ', 0)
		fmt.Fprintf(w, "ID\tNAME\tCPU %%\tCPU TIME\tMEMORY\tDISK\tNET RX\tNET TX\tLAST ACTIVITY\tPRIORITY\n")

		for _, id := range ids {
			resp, err := c.GetVMUsage(ctx, &resourceapi.GetVMUsageRequest{VmID: id})
			if err != nil {
				fmt.Fprintf(os.Stderr, "error getting usage for %s: %v\n", id, err)
				continue
			}

			u := resp.Usage
			lastActivity := "-"
			if u.LastActivity > 0 {
				lastActivity = humanize.Time(time.Unix(0, u.LastActivity))
			}

			fmt.Fprintf(w, "%s\t%s\t%.1f%%\t%.2fs\t%s\t%s\t%s\t%s\t%s\t%s\n",
				u.ID,
				u.Name,
				u.CpuPercent,
				u.CpuSeconds,
				humanize.Bytes(u.MemoryBytes),
				humanize.Bytes(u.DiskBytes),
				humanize.Bytes(u.NetRxBytes),
				humanize.Bytes(u.NetTxBytes),
				lastActivity,
				formatPriority(u.Priority),
			)
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
