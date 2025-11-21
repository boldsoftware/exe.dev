package instances

import (
	"context"
	"fmt"
	"sync"

	"github.com/urfave/cli/v2"

	"exe.dev/cmd/exelet-ctl/helpers"
	api "exe.dev/pkg/api/exe/compute/v1"
)

var stopInstanceCommand = &cli.Command{
	Name:      "stop",
	Usage:     "stop a compute instance",
	ArgsUsage: "[ID]",
	Action: func(clix *cli.Context) error {
		c, err := helpers.GetClient(clix)
		if err != nil {
			return err
		}
		defer c.Close()

		ctx := context.WithoutCancel(clix.Context)
		wg := &sync.WaitGroup{}
		for _, id := range clix.Args().Slice() {
			wg.Add(1)

			go func(id string, wg *sync.WaitGroup) {
				defer wg.Done()

				req := &api.StopInstanceRequest{
					ID: id,
				}

				if _, err := c.StopInstance(ctx, req); err != nil {
					fmt.Printf("ERR: error stopping %s: %s\n", id, err)
					return
				}

				fmt.Println(id)
			}(id, wg)
		}

		wg.Wait()

		return nil
	},
}
