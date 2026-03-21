package tiers

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/dustin/go-humanize"
	"github.com/urfave/cli/v2"

	"exe.dev/cmd/exelet-ctl/helpers"
	api "exe.dev/pkg/api/exe/compute/v1"
)

var listCommand = &cli.Command{
	Name:  "list",
	Usage: "List all configured storage tiers and their capacity",
	Action: func(clix *cli.Context) error {
		c, err := helpers.GetClient(clix)
		if err != nil {
			return err
		}
		defer c.Close()

		ctx := context.WithoutCancel(clix.Context)
		resp, err := c.ListStorageTiers(ctx, &api.ListStorageTiersRequest{})
		if err != nil {
			return fmt.Errorf("failed to list storage tiers: %w", err)
		}

		if len(resp.Tiers) == 0 {
			fmt.Println("No storage tiers configured.")
			return nil
		}

		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "NAME\tSIZE\tUSED\tAVAIL\tINSTANCES\tPRIMARY\tMETADATA")
		for _, tier := range resp.Tiers {
			primary := ""
			if tier.Primary {
				primary = "*"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%s\t%s\n",
				tier.Name,
				humanize.Bytes(tier.SizeBytes),
				humanize.Bytes(tier.UsedBytes),
				humanize.Bytes(tier.AvailableBytes),
				tier.InstanceCount,
				primary,
				formatMetadata(tier.Metadata),
			)
		}
		tw.Flush()

		return nil
	},
}

func formatMetadata(md map[string]string) string {
	if len(md) == 0 {
		return ""
	}
	keys := make([]string, 0, len(md))
	for k := range md {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	pairs := make([]string, len(keys))
	for i, k := range keys {
		pairs[i] = k + "=" + md[k]
	}
	return strings.Join(pairs, ", ")
}
