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

	"exe.dev/exepipe/internal/cmds"
	"exe.dev/stage"

	"github.com/prometheus/client_golang/prometheus"
)

// PipeConfig is the exepipe configuration details.
type PipeConfig struct {
	UnixAddr        *net.UnixAddr // where to listen for commands
	HTTPPort        string        // for metrics; "" for none, "0" for any
	Env             stage.Env
	Logger          *slog.Logger
	MetricsRegistry *prometheus.Registry
	DialFunc        DialFunc // optional: custom dialer (e.g. netns-aware); nil for default
}

// PipeInstance is the running exepipe instance.
type PipeInstance struct {
	cfg *PipeConfig

	cmdLoop    *cmdLoop
	piping     *piping
	httpServer *exepipeHTTPServer

	metrics *metrics

	lg *slog.Logger

	// transferringNew is true when we are in the process of
	// transferring from an old exepipe to this newly started one.
	// This will be set false when the old exepipe sends a
	// "transferred" command.
	transferringNew atomic.Bool

	// transferringOld is true on an old exepipe when it receives a
	// "transfer" command. This will never be set false.
	transferringOld atomic.Bool

	// transferredOld is true on an old exepipe when a
	// "transfer" command has been received and is complete.
	// When the number of connections drops to zero,
	// exepipe will exit.
	transferredOld atomic.Bool

	stopped  atomic.Bool
	stopChan chan bool // closed when stopping
}

// NewPipe creates a new exepipe instance.
func NewPipe(cfg *PipeConfig) (*PipeInstance, error) {
	lg := cfg.Logger
	if lg == nil {
		lg = slog.Default()
	}

	transferringNew := false

	ul, err := net.ListenUnix("unixpacket", cfg.UnixAddr)
	if err != nil {
		if !errors.Is(err, syscall.EADDRINUSE) {
			return nil, fmt.Errorf("failed to open unix socket %s: %v", cfg.UnixAddr, err)
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		ul, err = transferExepipeCmd(ctx, lg, cfg.UnixAddr)
		if err != nil {
			return nil, err
		}
		transferringNew = true
	}

	metrics := newMetrics(cfg.MetricsRegistry)

	pi := &PipeInstance{
		cfg:      cfg,
		metrics:  metrics,
		lg:       lg,
		stopChan: make(chan bool),
	}
	if transferringNew {
		pi.transferringNew.Store(true)
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

	ctx := context.Background()

	pi.cmdLoop.stop(ctx)
	pi.piping.stop(ctx)
	pi.httpServer.stop(ctx)

	pi.lg.DebugContext(ctx, "exepipe stopped")

	close(pi.stopChan)
}

// transferExepipeCmd asks a running exepipe to transfer the
// listener to this process. This is how we cleanly start up
// a new exepipe: the new exepipe asks the old one to transfer control.
// Any new commands will come to the new exepipe.
// The old exepipe will transfer all listeners, using "listen" commands.
// The old exepipe will keep handling existing copy commands
// until they are complete.
func transferExepipeCmd(ctx context.Context, lg *slog.Logger, addr *net.UnixAddr) (*net.UnixListener, error) {
	var d net.Dialer
	uc, err := d.DialUnix(ctx, "unixpacket", nil, addr)
	if err != nil {
		return nil, err
	}
	defer uc.Close()

	data, err := cmds.TransferCmd()
	if err != nil {
		return nil, err
	}

	n, oobn, err := uc.WriteMsgUnix(data, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("error sending to old exepipe: %w", err)
	}

	if n != len(data) || oobn != 0 {
		return nil, fmt.Errorf("short write to old exepipe: wrote %d, %d out of %d, %d", n, oobn, len(data), 0)
	}

	var rdata, roob [1024]byte
	n, oobn, _, _, err = uc.ReadMsgUnix(rdata[:], roob[:])
	if err != nil {
		return nil, fmt.Errorf("error receiving from old exepipe: %w", err)
	}

	ack, fd, err := cmds.UnmarshalTransferResponse(ctx, rdata[:n], roob[:oobn])
	if err != nil {
		return nil, fmt.Errorf("error unmarshaling transfer response from old exepipe: %w", err)
	}
	if ack != "" {
		return nil, errors.New(ack)
	}
	if fd == -1 {
		return nil, errors.New("old exepipe transfer response did not include descriptor")
	}

	f := os.NewFile(uintptr(fd), "listener")
	fln, err := net.FileListener(f)
	f.Close()
	if err != nil {
		return nil, fmt.Errorf("old exepipe transfer: new.FileListener failed: %v", err)
	}

	ln, ok := fln.(*net.UnixListener)
	if !ok {
		return nil, fmt.Errorf("old exepipe transfer listener has wrong type: got %T want %T", fln, (*net.UnixListener)(nil))
	}

	// Read the final response packet.
	n, oobn, _, _, err = uc.ReadMsgUnix(rdata[:], roob[:])
	if err != nil {
		return nil, fmt.Errorf("error receiving second response from old exepipe: %w", err)
	}

	ack, err = cmds.UnmarshalResponse(ctx, lg, rdata[:n], roob[:oobn])
	if err != nil {
		return nil, fmt.Errorf("error unmarshaling second response from old exepipe: %w", err)
	}
	if ack != "" {
		return nil, errors.New(ack)
	}

	return ln, nil
}

// transferListeners is called by the old exepipe to transfer
// the listeners to the new exepipe.
func (pi *PipeInstance) transferListeners(ctx context.Context) {
	// Open a connection to the new exepipe.
	var d net.Dialer
	uc, err := d.DialUnix(ctx, "unixpacket", nil, pi.cfg.UnixAddr)
	if err != nil {
		pi.lg.ErrorContext(ctx, "failed to connect to new exepipe", "addr", pi.cfg.UnixAddr, "error", err)
		return
	}
	defer uc.Close()

	pi.piping.transferListeners(ctx, pi.lg, uc)

	pi.transferredOld.Store(true)

	// If there are no active copy connections, we can exit now.
	if pi.piping.connsCount() == 0 {
		pi.lg.InfoContext(ctx, "exiting after transfer as there are no active copy connections")
		pi.Stop()
	}
}
