package instances

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/urfave/cli/v2"

	"exe.dev/cmd/exelet-ctl/helpers"
	api "exe.dev/pkg/api/exe/compute/v1"
)

var setGroupInstanceCommand = &cli.Command{
	Name:  "set-group",
	Usage: "set the group ID for an instance (for per-account cgroup grouping)",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:     "group",
			Aliases:  []string{"g"},
			Usage:    "group ID (account ID) to assign to the instance",
			Required: true,
		},
	},
	ArgsUsage: "[ID...]",
	Action: func(clix *cli.Context) error {
		c, err := helpers.GetClient(clix)
		if err != nil {
			return err
		}
		defer c.Close()

		groupID := clix.String("group")

		ctx := context.WithoutCancel(clix.Context)
		wg := &sync.WaitGroup{}
		var failCount atomic.Int32

		for _, id := range clix.Args().Slice() {
			wg.Add(1)

			go func(id string, wg *sync.WaitGroup) {
				defer wg.Done()

				req := &api.SetInstanceGroupRequest{
					ID:      id,
					GroupID: groupID,
				}

				if _, err := c.SetInstanceGroup(ctx, req); err != nil {
					fmt.Printf("ERR: error setting group for %s: %s\n", id, err)
					failCount.Add(1)
					return
				}

				fmt.Printf("%s group set to %s\n", id, groupID)
			}(id, wg)
		}

		wg.Wait()

		if n := failCount.Load(); n > 0 {
			return fmt.Errorf("failed to set group for %d instance(s)", n)
		}

		return nil
	},
}
