package main

import (
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/urfave/cli/v2"
)

func runAction(clix *cli.Context) error {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM, syscall.SIGUSR1)
	doneCh := make(chan bool, 1)
	errCh := make(chan error)

	addr := clix.String("listen-addr")
	hostKeyPath := clix.String("host-key")
	authorizedKeysPath := clix.String("authorized-keys")

	// start ssh server
	go func() {
		if err := startSSHServer(addr, hostKeyPath, authorizedKeysPath); err != nil {
			errCh <- err
		}
	}()

	go func() {
		for sig := range signals {
			switch sig {
			case syscall.SIGTERM, syscall.SIGINT:
				slog.Info("shutting down")
				doneCh <- true
			default:
				slog.Warn("unhandled signal", "signal", sig)
			}
		}
	}()

	select {
	case err := <-errCh:
		slog.Error(err.Error())
		return err
	case <-doneCh:
	}

	return nil
}
