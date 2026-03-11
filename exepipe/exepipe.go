// Package exepipe is a simple program that copies data between
// file descriptors. When exeprox or exed wants to set up a
// long-running connection, they will do so by handing the
// descriptors over to exepipe.
// exepipe will transfer data back and forth.
// This means that we can restart exed or exeprox without disturbing
// the existing connections.
package exepipe

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"

	"exe.dev/stage"

	"github.com/prometheus/client_golang/prometheus"
)

// PipeConfig is the exepipe configuration details.
type PipeConfig struct {
	UnixAddr        *net.UnixAddr // where to listen for commands
	HTTPPort        string        // for metrics; "" for none, "0" for any
	Env             *stage.Env
	Logger          *slog.Logger
	MetricsRegistry *prometheus.Registry
}

// PipeInstance is the running exepipe instance.
type PipeInstance struct {
	cmdLoop    *cmdLoop
	piping     *piping
	httpServer *exepipeHTTPServer

	metrics *metrics

	lg *slog.Logger

	stopped  atomic.Bool
	stopChan chan bool // closed when stopping
}

// NewPipe creates a new exepipe instance.
func NewPipe(cfg *PipeConfig) (*PipeInstance, error) {
	lg := cfg.Logger
	if lg == nil {
		lg = slog.Default()
	}

	ul, err := net.ListenUnix("unixpacket", cfg.UnixAddr)
	if err != nil {
		return nil, fmt.Errorf("failed to open unix socket %s: %v", cfg.UnixAddr, err)
	}

	metrics := newMetrics(cfg.MetricsRegistry)

	pi := &PipeInstance{
		metrics:  metrics,
		lg:       lg,
		stopChan: make(chan bool),
	}

	pi.cmdLoop, err = setupCmdLoop(cfg, pi, ul)
	if err != nil {
		return nil, err
	}

	pi.piping, err = setupPiping(cfg, pi)
	if err != nil {
		return nil, err
	}

	pi.httpServer, err = setupHTTPServer(cfg, pi)
	if err != nil {
		return nil, err
	}

	return pi, nil
}

// Start starts the exepipe server.
// This method does not return until something has told exepipe to stop.
func (pi *PipeInstance) Start() error {
	select {
	case <-pi.stopChan:
		return errors.New("exepipe: invalid Start after Stop")
	default:
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	if err := pi.piping.start(ctx); err != nil {
		return err
	}

	if err := pi.cmdLoop.start(ctx); err != nil {
		return err
	}

	if err := pi.httpServer.start(ctx); err != nil {
		return err
	}

	pi.lg.InfoContext(ctx, "server started")

	select {
	case sig := <-sigChan:
		pi.lg.InfoContext(ctx, "shutting down exepipe due to signal", "signal", sig)
		pi.Stop()
		return nil

	case <-ctx.Done():
		pi.lg.ErrorContext(ctx, "exepipe startup failed, shutting down")
		pi.Stop()
		return errors.New("server startup failed")

	case <-pi.stopChan:
		return nil // Stop called
	}
}

// Stop stops the exepipe server.
func (pi *PipeInstance) Stop() {
	if pi.stopped.Swap(true) {
		return
	}

	close(pi.stopChan)

	ctx := context.Background()

	pi.cmdLoop.stop(ctx)
	pi.piping.stop(ctx)
	pi.httpServer.stop(ctx)

	pi.lg.DebugContext(ctx, "exepipe stopped")
}
