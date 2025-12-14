package instances

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"text/tabwriter"

	"github.com/dustin/go-humanize"
	"github.com/urfave/cli/v2"

	"exe.dev/cmd/exelet-ctl/helpers"
	api "exe.dev/pkg/api/exe/compute/v1"
	resourceapi "exe.dev/pkg/api/exe/resource/v1"
)

var listInstancesCommand = &cli.Command{
	Name:    "list",
	Aliases: []string{"ls"},
	Usage:   "List compute instances",
	Action: func(clix *cli.Context) error {
		c, err := helpers.GetClient(clix)
		if err != nil {
			return err
		}
		defer c.Close()

		ctx := context.WithoutCancel(clix.Context)
		stream, err := c.ListInstances(ctx, &api.ListInstancesRequest{})
		if err != nil {
			return err
		}

		w := tabwriter.NewWriter(os.Stdout, 12, 1, 3, ' ', 0)
		fmt.Fprintf(w, "ID\tNAME\tIMAGE\tCPUS\tMEMORY\tDISK\tIP\tSTATE\tPRIORITY\n")
		for {
			resp, err := stream.Recv()
			if err != nil {
				if err == io.EOF {
					break
				}
				return err
			}

			i := resp.Instance
			cpus := ""
			memory := ""
			disk := ""
			ip := ""
			priority := "-"

			if v := i.VMConfig; v != nil {
				cpus = fmt.Sprintf("%d", v.CPUs)
				memory = humanize.Bytes(v.Memory)
				disk = humanize.Bytes(v.Disk)
				if iface := v.NetworkInterface; iface != nil {
					if x := iface.IP; x != nil {
						pip, _, err := net.ParseCIDR(x.IPV4)
						if err != nil {
							return err
						}
						ip = pip.String()
					}
				}
			}

			// Get priority from resource manager (only for running instances)
			if i.State == api.VMState_RUNNING {
				if usageResp, err := c.GetVMUsage(ctx, &resourceapi.GetVMUsageRequest{VmID: i.ID}); err == nil {
					priority = formatPriority(usageResp.Usage.Priority)
				}
			}

			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				i.ID,
				i.Name,
				i.Image,
				cpus,
				memory,
				disk,
				ip,
				i.State.String(),
				priority,
			)
		}
		if err := w.Flush(); err != nil {
			return err
		}

		return nil
	},
}
