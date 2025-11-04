package instances

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"regexp"
	"syscall"

	"github.com/urfave/cli/v2"

	"exe.dev/cmd/exelet-ctl/helpers"
	api "exe.dev/pkg/api/exe/compute/v1"
)

const ansiCodes = "[\u001B\u009B][[\\]()#;?]*(?:(?:(?:[a-zA-Z\\d]*(?:;[a-zA-Z\\d]*)*)?\u0007)|(?:(?:\\d{1,4}(?:;\\d{0,4})*)?[\\dA-PRZcf-ntqry=><~]))"

var instanceLogsCommand = &cli.Command{
	Name:      "logs",
	Usage:     "get instance logs",
	ArgsUsage: "[ID]",
	Flags:     []cli.Flag{},
	Action: func(clix *cli.Context) error {
		ctx := context.Background()
		instanceID := clix.Args().First()

		c, err := helpers.GetClient(clix)
		if err != nil {
			return err
		}
		defer c.Close()

		signals := make(chan os.Signal, 1)
		signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)

		stream, err := c.GetInstanceLogs(ctx, &api.GetInstanceLogsRequest{
			ID: instanceID,
		})
		if err != nil {
			return err
		}

		defer func() {
			stream.CloseSend()
		}()

		doneCh := make(chan struct{})
		errCh := make(chan error)

		go func() {
			defer close(doneCh)

			for {
				resp, err := stream.Recv()
				if err != nil {
					if err == io.EOF {
						break
					}

					errCh <- err
					return
				}

				log := resp.Log

				// clean known ansi codes from log messages
				if _, err := fmt.Fprint(os.Stdout, clean(log.Message)); err != nil {
					errCh <- err
					return
				}
			}
		}()

		for {
			select {
			case <-doneCh:
				return nil
			case err := <-errCh:
				return err
			case sig := <-signals:
				switch sig {
				case syscall.SIGTERM, syscall.SIGINT:
					return nil
				default:
				}
			}
		}
	},
}

func clean(v string) string {
	re := regexp.MustCompile(ansiCodes)
	return re.ReplaceAllString(v, "")
}
