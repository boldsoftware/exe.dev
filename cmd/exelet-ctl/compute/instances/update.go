package instances

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/urfave/cli/v2"

	"exe.dev/cmd/exelet-ctl/helpers"
	api "exe.dev/pkg/api/exe/compute/v1"
	resourceapi "exe.dev/pkg/api/exe/resource/v1"
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
		&cli.StringFlag{
			Name:    "priority",
			Aliases: []string{"p"},
			Usage:   "set instance priority (normal, low, auto)",
		},
	},
	ArgsUsage: "[ID...]",
	Action: func(clix *cli.Context) error {
		c, err := helpers.GetClient(clix)
		if err != nil {
			return err
		}
		defer c.Close()

		kernelImage := clix.String("kernel-image")
		initImage := clix.String("init-image")
		priorityStr := clix.String("priority")

		// Parse priority if specified
		var priority resourceapi.VMPriority
		var hasPriority bool
		if priorityStr != "" {
			hasPriority = true
			switch strings.ToLower(priorityStr) {
			case "normal":
				priority = resourceapi.VMPriority_PRIORITY_NORMAL
			case "low":
				priority = resourceapi.VMPriority_PRIORITY_LOW
			case "auto":
				priority = resourceapi.VMPriority_PRIORITY_AUTO
			default:
				return fmt.Errorf("invalid priority %q: must be 'normal', 'low', or 'auto'", priorityStr)
			}
		}

		ctx := context.WithoutCancel(clix.Context)
		wg := &sync.WaitGroup{}
		for _, id := range clix.Args().Slice() {
			wg.Add(1)

			go func(id string, wg *sync.WaitGroup) {
				defer wg.Done()

				// Update instance config if kernel or init image specified
				if kernelImage != "" || initImage != "" {
					req := &api.UpdateInstanceRequest{
						ID:          id,
						KernelImage: kernelImage,
						InitImage:   initImage,
					}

					if _, err := c.UpdateInstance(ctx, req); err != nil {
						fmt.Printf("ERR: error updating %s: %s\n", id, err)
						return
					}
				}

				// Set priority if specified
				if hasPriority {
					req := &resourceapi.SetVMPriorityRequest{
						VmID:     id,
						Priority: priority,
					}

					if _, err := c.SetVMPriority(ctx, req); err != nil {
						fmt.Printf("ERR: error setting priority for %s: %s\n", id, err)
						return
					}
				}

				fmt.Println(id)
			}(id, wg)
		}

		wg.Wait()

		return nil
	},
}
