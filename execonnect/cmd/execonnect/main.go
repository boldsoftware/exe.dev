// Command execonnect connects private customer endpoints to exe.dev.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	execonnectcli "github.com/boldsoftware/exe.dev/execonnect/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := execonnectcli.Run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		if errors.Is(err, context.Canceled) && ctx.Err() != nil {
			os.Exit(130)
		}
		fmt.Fprintln(os.Stderr, "execonnect:", err)
		os.Exit(1)
	}
}
