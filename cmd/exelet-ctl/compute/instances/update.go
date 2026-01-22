package instances

import (
	"context"
	"fmt"
	"sync"

	"github.com/urfave/cli/v2"

	"exe.dev/cmd/exelet-ctl/helpers"
	api "exe.dev/pkg/api/exe/compute/v1"
)

var updateInstanceCommand = &cli.Command{
	Name:  "update",
	Usage: "update a compute instance",
	Flags: []cli.Flag{
		&cli.BoolFlag{
			Name:    "kernel",
			Aliases: []string{"k"},
			Usage:   "update kernel to latest embedded version",
		},
	},
	ArgsUsage: "[ID...]",
	Action: func(clix *cli.Context) error {
		c, err := helpers.GetClient(clix)
		if err != nil {
			return err
		}
		defer c.Close()

		kernel := clix.Bool("kernel")

		ctx := context.WithoutCancel(clix.Context)
		wg := &sync.WaitGroup{}
		for _, id := range clix.Args().Slice() {
			wg.Add(1)

			go func(id string) {
				defer wg.Done()

				req := &api.UpdateInstanceRequest{
					ID:     id,
					Kernel: kernel,
				}

				if _, err := c.UpdateInstance(ctx, req); err != nil {
					fmt.Printf("ERR: error updating %s: %s\n", id, err)
					return
				}

				fmt.Println(id)
			}(id)
		}

		wg.Wait()

		return nil
	},
}
