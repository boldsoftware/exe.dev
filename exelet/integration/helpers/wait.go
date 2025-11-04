package helpers

import (
	"context"
	"io"
	"strings"
	"time"

	api "exe.dev/pkg/api/exe/compute/v1"
)

// WaitForOutput waits for the output for the specified id
// To add a timeout, use a context with timeout.
func WaitForOutput(ctx context.Context, id, v string) error {
	c, err := GetClient()
	if err != nil {
		return err
	}
	defer c.Close()

	logTicker := time.NewTicker(time.Second * 1)

	readyCh := make(chan struct{})
	errCh := make(chan error)

	// start log reader to check log output
	go func() {
		defer logTicker.Stop()

		for range logTicker.C {
			stream, err := c.GetInstanceLogs(ctx, &api.GetInstanceLogsRequest{
				ID: id,
			})
			if err != nil {
				errCh <- err
				return
			}
			for {
				r, err := stream.Recv()
				if err != nil {
					if err == io.EOF {
						break
					}
					errCh <- err
					return
				}
				if strings.Contains(r.Log.Message, v) {
					close(readyCh)
					return
				}
			}
		}
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-errCh:
		return err
	case <-readyCh:
	}

	return nil
}
