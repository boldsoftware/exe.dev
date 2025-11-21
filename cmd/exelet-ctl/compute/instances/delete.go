package instances

import (
	"context"
	"fmt"
	"sync"

	"github.com/urfave/cli/v2"

	"exe.dev/cmd/exelet-ctl/helpers"
	api "exe.dev/pkg/api/exe/compute/v1"
)

var deleteInstanceCommand = &cli.Command{
	Name:      "delete",
	Aliases:   []string{"rm"},
	Usage:     "Remove a compute instance",
	ArgsUsage: "[ID]",
	Flags: []cli.Flag{
		&cli.BoolFlag{
			Name:  "force",
			Usage: "force removal of instance",
		},
	},
	Action: func(clix *cli.Context) error {
		c, err := helpers.GetClient(clix)
		if err != nil {
			return err
		}
		defer c.Close()

		ctx := context.WithoutCancel(clix.Context)
		wg := &sync.WaitGroup{}
		force := clix.Bool("force")
		for _, id := range clix.Args().Slice() {
			wg.Add(1)

			go func(id string, wg *sync.WaitGroup) {
				defer wg.Done()

				req := &api.DeleteInstanceRequest{
					ID:    id,
					Force: force,
				}

				if _, err := c.DeleteInstance(ctx, req); err != nil {
					fmt.Printf("ERR: error removing %s: %s\n", id, err)
					return
				}

				fmt.Println(id)
			}(id, wg)
		}

		wg.Wait()

		return nil
	},
}
