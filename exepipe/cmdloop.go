package exepipe

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"

	"exe.dev/exepipe/internal/cmds"
)

// cmdLoop reads commands from a Unix socket.
type cmdLoop struct {
	listener     atomic.Pointer[net.UnixListener]
	pipeInstance *PipeInstance

	connsMu sync.Mutex
	conns   map[*net.UnixConn]bool
}

// setupCmdLoop prepares a cmdLoop.
func setupCmdLoop(cfg *PipeConfig, pi *PipeInstance, ul *net.UnixListener) (*cmdLoop, error) {
	cl := &cmdLoop{
		pipeInstance: pi,
		conns:        make(map[*net.UnixConn]bool),
	}
	cl.listener.Store(ul)
	return cl, nil
}

// start starts a goroutine that reads and dispatches commands.
func (cl *cmdLoop) start(ctx context.Context) error {
	go cl.acceptLoop(context.WithoutCancel(ctx))
	return nil
}

// stop stops the command loop.
func (cl *cmdLoop) stop(ctx context.Context) {
	if ln := cl.listener.Load(); ln != nil {
		ln.Close()
		cl.listener.Store(nil)
	}
	cl.stopConnsExcept(ctx, nil)
}

// stopConns stops all active client connections,
// except for the keep if that is not nil.
func (cl *cmdLoop) stopConnsExcept(ctx context.Context, keep *net.UnixConn) {
	cl.connsMu.Lock()
	defer cl.connsMu.Unlock()
	for uc := range cl.conns {
		if uc != keep {
			uc.Close()
		}
	}
	clear(cl.conns)
	if keep != nil {
		cl.conns[keep] = true
	}
}

// acceptLoop is an endless loop that waits for a connection,
// and then processes commands sent over that connection.
// This runs in a separate goroutine.
func (cl *cmdLoop) acceptLoop(ctx context.Context) {
	for {
		if cl.pipeInstance.transferringOld.Load() {
			// We are transferring and should no
			// longer accept new connections.
			return
		}

		ln := cl.listener.Load()
		if ln == nil {
			// The listener is closed.
			return
		}

		uc, err := ln.AcceptUnix()
		if err != nil {
			if !errors.Is(err, net.ErrClosed) {
				cl.pipeInstance.lg.ErrorContext(ctx, "exepipe unix listener closed", "error", err)
			}
			return
		}

		// Keep track of the connections so that we can close them.
		cl.connsMu.Lock()
		cl.conns[uc] = true
		cl.connsMu.Unlock()

		go cl.commandLoop(ctx, uc)
	}
}

// commandLoop processes commands sent over a connection.
func (cl *cmdLoop) commandLoop(ctx context.Context, uc *net.UnixConn) {
	actions := cl.actions(uc)
	for {
		var buf, oob [1024]byte
		n, oobn, _, _, err := uc.ReadMsgUnix(buf[:], oob[:])
		if err != nil {
			// We should be able to check err != io.EOF here,
			// but there is a bug in the Go standard library
			// as of 1.26: https://go.dev/issue/78137.
			if !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
				cl.pipeInstance.lg.ErrorContext(ctx, "exepipe unix socket read failure", "error", err)
				uc.Close()
			}
			cl.connsMu.Lock()
			delete(cl.conns, uc)
			cl.connsMu.Unlock()
			return
		}

		err = cmds.Dispatch(ctx, cl.pipeInstance.lg, actions, buf[:n], oob[:oobn])

		ack := ""
		if err != nil {
			cl.pipeInstance.lg.ErrorContext(ctx, "exepipe action failure", "action", string(buf[:n]), "oob", string(oob[:oobn]), "error", err)
			ack = err.Error()
		}

		data, err := cmds.MarshalResponse(ctx, cl.pipeInstance.lg, ack)
		if err != nil {
			cl.pipeInstance.lg.ErrorContext(ctx, "exepipe response marshaling failed", "ack", ack, "error", err)
		}

		n, oobn, err = uc.WriteMsgUnix(data, nil, nil)
		if err != nil || n != len(data) || oobn != 0 {
			cl.pipeInstance.lg.ErrorContext(ctx, "exepipe unix socket write failure", "tried", len(data), "wrote", n, "oobn", oobn, "error", err)
		}
	}
}

// actions returns a map from commands to the functions that implement
// those commands.
func (cl *cmdLoop) actions(uc *net.UnixConn) cmds.Actions {
	ca := &cmdActor{
		pipeInstance: cl.pipeInstance,
		uc:           uc,
	}
	return cmds.Actions{
		"copy":        ca.copyAction,
		"listen":      ca.listenAction,
		"unlisten":    ca.unlistenAction,
		"listeners":   ca.listenersAction,
		"transfer":    ca.transferAction,
		"transferred": ca.transferredAction,
	}
}

// cmdActor handles commands.
type cmdActor struct {
	pipeInstance *PipeInstance
	uc           *net.UnixConn
}

// copyAction implements the copy command.
// This copies data between two file descriptors.
func (ca *cmdActor) copyAction(ctx context.Context, key string, fds []int, host string, port int, typ, _ string) error {
	if key != "" {
		return fmt.Errorf("unexpected key to copy command: %q", key)
	}
	if host != "" || port != 0 {
		return fmt.Errorf("unexpected destination to copy command: %q %d", host, port)
	}
	if len(fds) != 2 {
		return fmt.Errorf("copy command received %d file descriptors, expected 2", len(fds))
	}

	ca.pipeInstance.piping.Copy(ctx, fds[0], fds[1], typ)

	return nil
}

// listenAction implements the listen command.
// This listens on a socket. When a connection arrives,
// this opens a connection to the destination and then
// copies all between the two file descriptors.
func (ca *cmdActor) listenAction(ctx context.Context, key string, fds []int, host string, port int, typ, netns string) error {
	if key == "" {
		return errors.New("missing key to listen command")
	}
	if host == "" || port == 0 {
		return errors.New("missing destination to listen command")
	}
	if len(fds) != 1 {
		return fmt.Errorf("listen command received %d file descriptors, expected 1", len(fds))
	}

	ca.pipeInstance.piping.Listen(ctx, key, fds[0], host, port, typ, netns)

	return nil
}

// unlistenAction implements the unlisten command.
// This disables an existing listener.
func (ca *cmdActor) unlistenAction(ctx context.Context, key string, fds []int, host string, port int, typ, _ string) error {
	if key == "" {
		return errors.New("missing key to unlisten command")
	}
	if len(fds) > 0 || host != "" || port != 0 || typ != "" {
		return errors.New("unexpected arguments to unlisten command")
	}
	return ca.pipeInstance.piping.Unlisten(ctx, key)
}

// listenersAction implements the listeners command.
// This sends all the current listeners on the socket.
func (ca *cmdActor) listenersAction(ctx context.Context, key string, fds []int, host string, port int, typ, _ string) error {
	if key != "" || len(fds) > 0 || host != "" || port != 0 || typ != "" {
		return errors.New("unexpected arguments to listeners command")
	}

	listeners := ca.pipeInstance.piping.allListeners()
	var err error
	for data := range cmds.MarshalListenersResponse(ctx, ca.pipeInstance.lg, listeners, &err) {
		n, oobn, err := ca.uc.WriteMsgUnix(data, nil, nil)
		if err != nil || n != len(data) || oobn != 0 {
			ca.pipeInstance.lg.ErrorContext(ctx, "exepipe unix socket write failure", "tried", len(data), "wrote", n, "oobn", oobn, "error", err)
			return errors.New("exepipe unix socket write failure")
		}
	}

	return err
}

// transferAction implements the transfer command.
// This will be used by a new exepipe to tell an old exepipe
// that the new one is taking over.
// Clients will not send this command.
//
// The old exepipe (the process running this method) will send
// over the command listener, and stop listening on it.
// The old exepipe will also send over all existing listeners.
// The old exepipe will continue processing copy connections
// until they are done, at which point it will exit.
//
// In the normal case this method will send one response with the listener,
// and then another response indicating command success.
// This follows the pattern of other commands.
func (ca *cmdActor) transferAction(ctx context.Context, key string, fds []int, host string, port int, typ, _ string) error {
	if key != "" || len(fds) > 0 || host != "" || port != 0 || typ != "" {
		return errors.New("unexpected arguments to transfer command")
	}

	if ca.pipeInstance.transferringOld.Load() {
		ca.pipeInstance.lg.ErrorContext(ctx, "exepipe transfer already in progress")
		return errors.New("transfer already in progress")
	}

	ca.pipeInstance.transferringOld.Store(true)

	// Stop active clients. They should reconnect to the new exepipe.
	ca.pipeInstance.cmdLoop.stopConnsExcept(ctx, ca.uc)

	// Transfer the command listener to the new exepipe.
	ln := ca.pipeInstance.cmdLoop.listener.Load()
	if ln == nil {
		ca.pipeInstance.lg.ErrorContext(ctx, "exepipe transfer failed: listener closed")
		return errors.New("listener closed")
	}

	data, oob, err := cmds.MarshalTransferResponse(ctx, ca.pipeInstance.lg, "", ln)
	if err != nil {
		return err
	}

	n, oobn, err := ca.uc.WriteMsgUnix(data, oob, nil)
	if err != nil || n != len(data) || oobn != len(oob) {
		ca.pipeInstance.lg.ErrorContext(ctx, "exepipe unix socket write failure", "data", len(data), "oob", len(oob), "wrote", n, "oobn", oobn, "error", err)
		return errors.New("exepipe unix socket write failure")
	}

	// At this point we've written the listener to the socket,
	// so we can close the listener.
	ca.pipeInstance.cmdLoop.listener.Store(nil)
	ln.Close()

	// Transfer active listeners to the new exepipe.
	go ca.pipeInstance.transferListeners(context.WithoutCancel(ctx))

	return nil
}

// transferredAction handles the transferred command,
// which just lets the new exepipe know that all listeners
// have been sent over.
func (ca *cmdActor) transferredAction(ctx context.Context, key string, fds []int, host string, port int, typ, _ string) error {
	if key != "" || len(fds) > 0 || host != "" || port != 0 || typ != "" {
		return errors.New("unexpected arguments to transferred command")
	}
	ca.pipeInstance.transferringNew.Store(false)
	ca.pipeInstance.lg.InfoContext(ctx, "transferred all listeners from old exepipe")
	return nil
}
