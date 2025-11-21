package filesystems

import (
	"context"
	"fmt"

	"github.com/urfave/cli/v2"

	"exe.dev/cmd/exelet-ctl/helpers"
	api "exe.dev/pkg/api/exe/storage/v1"
)

var loadFilesystemCommand = &cli.Command{
	Name:      "load",
	Usage:     "load a filesystem from an image",
	ArgsUsage: "[IMAGE-REF]",
	Action: func(clix *cli.Context) error {
		c, err := helpers.GetClient(clix)
		if err != nil {
			return err
		}
		defer c.Close()

		ctx := context.WithoutCancel(clix.Context)

		ref := clix.Args().First()

		s := helpers.NewSpinner(fmt.Sprintf("loading %s", ref))
		s.Start()
		defer s.Stop()

		req := &api.LoadFilesystemRequest{
			Image: ref,
		}

		resp, err := c.LoadFilesystem(ctx, req)
		if err != nil {
			if api.IsResourceExists(err) {
				return nil
			}
			return fmt.Errorf("error loading filesystem %s: %s", ref, err)
		}

		// stop spinner
		s.Stop()

		fmt.Printf("%s: %s\n", ref, resp.ID)

		return nil
	},
}
