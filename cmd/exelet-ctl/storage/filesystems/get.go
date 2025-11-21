package filesystems

import (
	"context"
	"fmt"
	"os"
	"sync"
	"text/tabwriter"

	"github.com/dustin/go-humanize"
	"github.com/urfave/cli/v2"

	"exe.dev/cmd/exelet-ctl/helpers"
	api "exe.dev/pkg/api/exe/storage/v1"
)

var getFilesystemCommand = &cli.Command{
	Name:      "get",
	Usage:     "get info on a filesystem",
	ArgsUsage: "[ID]",
	Action: func(clix *cli.Context) error {
		c, err := helpers.GetClient(clix)
		if err != nil {
			return err
		}
		defer c.Close()

		ctx := context.WithoutCancel(clix.Context)
		filesystems := []*api.Filesystem{}
		wg := &sync.WaitGroup{}
		doneCh := make(chan struct{})
		errCh := make(chan error)

		for _, id := range clix.Args().Slice() {
			wg.Add(1)

			go func(id string, wg *sync.WaitGroup) {
				defer wg.Done()

				req := &api.GetFilesystemRequest{
					ID: id,
				}

				resp, err := c.GetFilesystem(ctx, req)
				if err != nil {
					errCh <- fmt.Errorf("error getting filesystem %s: %s", id, err)
					return
				}

				filesystems = append(filesystems, resp.Filesystem)
			}(id, wg)
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

		w := tabwriter.NewWriter(os.Stdout, 12, 1, 3, ' ', 0)
		fmt.Fprintf(w, "ID\tPATH\tSIZE\n")

		for _, fs := range filesystems {
			volSize := humanize.Bytes(fs.Size)
			fmt.Fprintf(w, "%s\t%s\t%s\n",
				fs.ID,
				fs.Path,
				volSize,
			)
		}
		if err := w.Flush(); err != nil {
			return err
		}

		return nil
	},
}
