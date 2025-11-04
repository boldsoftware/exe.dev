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

		ctx := context.Background()
		stream, err := c.ListInstances(ctx, &api.ListInstancesRequest{})
		if err != nil {
			return err
		}

		w := tabwriter.NewWriter(os.Stdout, 12, 1, 3, ' ', 0)
		fmt.Fprintf(w, "ID\tNAME\tIMAGE\tCPUS\tMEMORY\tDISK\tIP\tSTATE\n")
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

			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				i.ID,
				i.Name,
				i.Image,
				cpus,
				memory,
				disk,
				ip,
				i.State.String(),
			)
		}
		w.Flush()
		return nil
	},
}
