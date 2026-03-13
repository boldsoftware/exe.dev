package exepipe

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"

	"exe.dev/exepipe/internal/cmds"
)

// cmdLoop reads commands from a Unix socket.
type cmdLoop struct {
	listener     *net.UnixListener
	pipeInstance *PipeInstance

	connsMu sync.Mutex
	conns   map[*net.UnixConn]bool
}

// setupCmdLoop prepares a cmdLoop.
func setupCmdLoop(cfg *PipeConfig, pi *PipeInstance, ul *net.UnixListener) (*cmdLoop, error) {
	cl := &cmdLoop{
		listener:     ul,
		pipeInstance: pi,
		conns:        make(map[*net.UnixConn]bool),
	}
	return cl, nil
}

// start starts a goroutine that reads and dispatches commands.
func (cl *cmdLoop) start(ctx context.Context) error {
	go cl.acceptLoop(context.WithoutCancel(ctx))
	return nil
}

// stop stops the command loop.
func (cl *cmdLoop) stop(ctx context.Context) {
	cl.listener.Close()

	cl.connsMu.Lock()
	defer cl.connsMu.Unlock()
	for uc := range cl.conns {
		uc.Close()
	}
	clear(cl.conns)
}

// acceptLoop is an endless loop that waits for a connection,
// and then processes commands sent over that connection.
// This runs in a separate goroutine.
func (cl *cmdLoop) acceptLoop(ctx context.Context) {
	for {
		uc, err := cl.listener.AcceptUnix()
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
		"copy":      ca.copyAction,
		"listen":    ca.listenAction,
		"unlisten":  ca.unlistenAction,
		"listeners": ca.listenersAction,
	}
}

// cmdActor handles commands.
type cmdActor struct {
	pipeInstance *PipeInstance
	uc           *net.UnixConn
}

// copyAction implements the copy command.
// This copies data between two file descriptors.
func (ca *cmdActor) copyAction(ctx context.Context, key string, fds []int, host string, port int, typ string) error {
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
func (ca *cmdActor) listenAction(ctx context.Context, key string, fds []int, host string, port int, typ string) error {
	if key == "" {
		return errors.New("missing key to listen command")
	}
	if host == "" || port == 0 {
		return errors.New("missing destination to listen command")
	}
	if len(fds) != 1 {
		return fmt.Errorf("listen command received %d file descriptors, expected 1", len(fds))
	}

	ca.pipeInstance.piping.Listen(ctx, key, fds[0], host, port, typ)

	return nil
}

// unlistenAction implements the unlisten command.
// This disables an existing listener.
func (ca *cmdActor) unlistenAction(ctx context.Context, key string, fds []int, host string, port int, typ string) error {
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
func (ca *cmdActor) listenersAction(ctx context.Context, key string, fds []int, host string, port int, typ string) error {
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
