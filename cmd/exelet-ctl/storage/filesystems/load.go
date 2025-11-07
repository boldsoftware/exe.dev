package filesystems

import (
	"context"
	"fmt"
	"os"
	"sync"
	"text/tabwriter"

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

		ctx := context.Background()
		wg := &sync.WaitGroup{}
		doneCh := make(chan struct{})
		errCh := make(chan error)

		filesystems := map[string]string{}

		s := helpers.NewSpinner("loading filesystems")
		s.Start()
		defer s.Stop()

		for _, ref := range clix.Args().Slice() {
			wg.Add(1)

			go func(id string, wg *sync.WaitGroup) {
				defer wg.Done()

				req := &api.LoadFilesystemRequest{
					Image: ref,
				}

				resp, err := c.LoadFilesystem(ctx, req)
				if err != nil {
					errCh <- fmt.Errorf("error loading filesystem %s: %s", id, err)
					return
				}
				filesystems[ref] = resp.ID
			}(ref, wg)
		}

		go func() {
			wg.Wait()
			close(doneCh)
		}()

		select {
		case <-doneCh:
		case err := <-errCh:
			return err
		}

		// stop spinner
		s.Stop()

		w := tabwriter.NewWriter(os.Stdout, 12, 1, 3, ' ', 0)
		fmt.Fprintf(w, "REF\tID\n")

		for ref, id := range filesystems {
			fmt.Fprintf(w, "%s\t%s\n",
				ref,
				id,
			)
		}
		if err := w.Flush(); err != nil {
			return err
		}

		return nil
	},
}
