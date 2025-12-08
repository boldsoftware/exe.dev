package instances

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"text/tabwriter"
	"time"

	"github.com/dustin/go-humanize"
	"github.com/urfave/cli/v2"

	"exe.dev/cmd/exelet-ctl/helpers"
	api "exe.dev/pkg/api/exe/compute/v1"
)

var getInstanceCommand = &cli.Command{
	Name:      "get",
	Usage:     "Get details on a compute instance",
	ArgsUsage: "<id>",
	Flags: []cli.Flag{
		&cli.BoolFlag{
			Name:    "json",
			Aliases: []string{"j"},
			Usage:   "Output in JSON format",
		},
	},
	Action: func(clix *cli.Context) error {
		if clix.NArg() < 1 {
			return fmt.Errorf("instance ID required")
		}

		c, err := helpers.GetClient(clix)
		if err != nil {
			return err
		}
		defer c.Close()

		ctx := context.WithoutCancel(clix.Context)
		id := clix.Args().First()

		resp, err := c.GetInstance(ctx, &api.GetInstanceRequest{ID: id})
		if err != nil {
			return fmt.Errorf("error getting instance %s: %w", id, err)
		}

		i := resp.Instance

		if clix.Bool("json") {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(i)
		}

		// Print instance details in a readable format
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)

		fmt.Fprintf(w, "ID:\t%s\n", i.ID)
		fmt.Fprintf(w, "Name:\t%s\n", i.Name)
		fmt.Fprintf(w, "Image:\t%s\n", i.Image)
		fmt.Fprintf(w, "State:\t%s\n", i.State.String())
		fmt.Fprintf(w, "Node:\t%s\n", i.Node)
		fmt.Fprintf(w, "SSH Port:\t%d\n", i.SSHPort)

		if i.CreatedAt > 0 {
			fmt.Fprintf(w, "Created:\t%s\n", time.Unix(0, i.CreatedAt).Format(time.RFC3339))
		}
		if i.UpdatedAt > 0 {
			fmt.Fprintf(w, "Updated:\t%s\n", time.Unix(0, i.UpdatedAt).Format(time.RFC3339))
		}

		if v := i.VMConfig; v != nil {
			fmt.Fprintf(w, "\nVM Config:\n")
			fmt.Fprintf(w, "  CPUs:\t%d\n", v.CPUs)
			fmt.Fprintf(w, "  Memory:\t%s\n", humanize.Bytes(v.Memory))
			fmt.Fprintf(w, "  Disk:\t%s\n", humanize.Bytes(v.Disk))
			if v.KernelPath != "" {
				fmt.Fprintf(w, "  Kernel:\t%s\n", v.KernelPath)
			}
			if v.RootDiskPath != "" {
				fmt.Fprintf(w, "  Root Disk:\t%s\n", v.RootDiskPath)
			}

			if iface := v.NetworkInterface; iface != nil {
				fmt.Fprintf(w, "\n  Network:\n")
				fmt.Fprintf(w, "    Interface:\t%s\n", iface.Name)
				if x := iface.IP; x != nil {
					if x.IPV4 != "" {
						pip, _, err := net.ParseCIDR(x.IPV4)
						if err == nil {
							fmt.Fprintf(w, "    IPv4:\t%s\n", pip.String())
						} else {
							fmt.Fprintf(w, "    IPv4:\t%s\n", x.IPV4)
						}
					}
					if x.GatewayV4 != "" {
						fmt.Fprintf(w, "    Gateway:\t%s\n", x.GatewayV4)
					}
				}
				if len(iface.Nameservers) > 0 {
					fmt.Fprintf(w, "    Nameservers:\t%v\n", iface.Nameservers)
				}
			}
		}

		if len(i.ExposedPorts) > 0 {
			fmt.Fprintf(w, "\nExposed Ports:\n")
			for _, p := range i.ExposedPorts {
				fmt.Fprintf(w, "  %d/%s\n", p.Port, p.Protocol)
			}
		}

		return w.Flush()
	},
}
