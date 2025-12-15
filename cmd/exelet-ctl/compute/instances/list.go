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

		// Collect all instances first
		stream, err := c.ListInstances(ctx, &api.ListInstancesRequest{})
		if err != nil {
			return err
		}

		var instances []*api.Instance
		for {
			resp, err := stream.Recv()
			if err != nil {
				if err == io.EOF {
					break
				}
				return err
			}
			instances = append(instances, resp.Instance)
		}

		// Fetch all VM usage in one streaming call and build a lookup map
		usageMap := make(map[string]*resourceapi.VMUsage)
		if usageStream, err := c.ListVMUsage(ctx, &resourceapi.ListVMUsageRequest{}); err == nil {
			for {
				resp, err := usageStream.Recv()
				if err != nil {
					if err == io.EOF {
						break
					}
					break // Ignore errors, just use empty map
				}
				if resp.Usage != nil {
					usageMap[resp.Usage.ID] = resp.Usage
				}
			}
		}

		// Print all instances, looking up priority from the map
		w := tabwriter.NewWriter(os.Stdout, 12, 1, 3, ' ', 0)
		fmt.Fprintf(w, "ID\tNAME\tIMAGE\tCPUS\tMEMORY\tDISK\tIP\tSTATE\tPRIORITY\n")

		for _, i := range instances {
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

			// Look up priority from the pre-fetched map
			if i.State == api.VMState_RUNNING {
				if usage, ok := usageMap[i.ID]; ok {
					priority = formatPriority(usage.Priority)
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
