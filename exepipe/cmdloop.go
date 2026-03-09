package exepipe

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
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
	actions := cl.actions()
	for {
		var buf, oob [256]byte
		n, oobn, _, _, err := uc.ReadMsgUnix(buf[:], oob[:])
		if err != nil {
			if !errors.Is(err, net.ErrClosed) {
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
func (cl *cmdLoop) actions() cmds.Actions {
	return cmds.Actions{
		"copy":   cl.copyAction,
		"listen": cl.listenAction,
	}
}

// copyAction implements the copy command.
// This copies data between two file descriptors.
func (cl *cmdLoop) copyAction(ctx context.Context, fds []int, arg, typ string) error {
	if arg != "" {
		return errors.New("unexpected argument to copy command")
	}
	if len(fds) != 2 {
		return fmt.Errorf("copy command received %d file descriptors, expected 2", len(fds))
	}

	cl.pipeInstance.piping.Copy(ctx, fds[0], fds[1], typ)

	return nil
}

// listenAction implements the listen command.
// This listens on a socket. When a connection arrives,
// this opens a connection to the destination and then
// copies all between the two file descriptors.
func (cl *cmdLoop) listenAction(ctx context.Context, fds []int, arg, typ string) error {
	if arg == "" {
		return errors.New("missing argument to listen command")
	}
	host, portStr, err := net.SplitHostPort(arg)
	if err != nil {
		return fmt.Errorf("listen command failed to parse %q: %v", arg, err)
	}
	if len(fds) != 1 {
		return fmt.Errorf("listen command received %d file descriptors, expected 1", len(fds))
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return fmt.Errorf("listen command port %q is not a number: %v", portStr, err)
	}

	cl.pipeInstance.piping.Listen(ctx, fds[0], host, port, typ)

	return nil
}
