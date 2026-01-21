package resources

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"reflect"
	"text/tabwriter"

	"github.com/urfave/cli/v2"

	"exe.dev/cmd/exelet-ctl/helpers"
	api "exe.dev/pkg/api/exe/resource/v1"
)

var machineCommand = &cli.Command{
	Name:   "machine",
	Usage:  "Get current machine resource usage",
	Flags:  []cli.Flag{
		&cli.BoolFlag{
			Name:     "json",
			Aliases: []string{"j"},
			Usage:   "Output in JSON format",
		},
	},
	Action: func(clix *cli.Context) error {
		c, err := helpers.GetClient(clix)
		if err != nil {
			return err
		}
		defer c.Close()

		ctx := context.WithoutCancel(clix.Context)

		usage, err := c.GetMachineUsage(ctx, &api.GetMachineUsageRequest{})
		if err != nil {
			return err
		}

		if clix.Bool("json") {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(usage)
		}

		// Flatten one level so that we show Available
		// and then the Usage fields at the same level.
		var showFields func(io.Writer, reflect.Value)
		showFields = func(w io.Writer, v reflect.Value) {
			typ := v.Type()
			for i := range v.NumField() {
				vf := v.Field(i)
				if !vf.CanInterface() {
					continue
				}
				name := typ.Field(i).Name
				if name == "Usage" {
					showFields(w, vf.Elem())
				} else {
					value := vf.Interface()
					fmt.Fprintf(w, "%s\t%v\n", name, value)
				}
			}
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		showFields(w, reflect.ValueOf(*usage))
		if err := w.Flush(); err != nil {
			return err
		}
		return nil
	},
}
