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
		&cli.StringFlag{
			Name:    "kernel-image",
			Aliases: []string{"k"},
			Usage:   "image to use for kernel",
		},
		&cli.StringFlag{
			Name:    "init-image",
			Aliases: []string{"i"},
			Usage:   "image to use for init",
		},
	},
	ArgsUsage: "[ID]",
	Action: func(clix *cli.Context) error {
		c, err := helpers.GetClient(clix)
		if err != nil {
			return err
		}
		defer c.Close()

		kernelImage := clix.String("kernel-image")
		initImage := clix.String("init-image")

		ctx := context.Background()
		wg := &sync.WaitGroup{}
		for _, id := range clix.Args().Slice() {
			wg.Add(1)

			go func(id string, wg *sync.WaitGroup) {
				defer wg.Done()

				req := &api.UpdateInstanceRequest{
					ID:          id,
					KernelImage: kernelImage,
					InitImage:   initImage,
				}

				if _, err := c.UpdateInstance(ctx, req); err != nil {
					fmt.Printf("ERR: error updating %s: %s\n", id, err)
					return
				}

				fmt.Println(id)
			}(id, wg)
		}

		wg.Wait()

		return nil
	},
}
