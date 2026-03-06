// Package cmds defines the commands passed to exepipe.
// Commands are JSON encoded data, and may include file descriptors
// passed as ancillary data.
package cmds

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"syscall"
)

// cmd is a single command sent to exepipe.
type cmd struct {
	Action string `json:"action"`         // what to do
	Arg    string `json:"dest,omitempty"` // where to connect to host:port
	Type   string `json:"type"`           // connection type
}

// CopyCmd returns a marshalled command to copy
// data from one network connection to another
// The marshalled command includes both regular data and oob data,
// to be sent over a Unix socket.
// The caller must ensure that the network connections
// are not closed until the data has been sent to exepipe.
// The typ argument is for metrics;
// it is expected to be something like "http" or "ssh".
func CopyCmd(c1, c2 net.Conn, typ string) (data, oob []byte, err error) {
	cm := cmd{
		Action: "copy",
		Type:   typ,
	}

	data, err = json.Marshal(cm)
	if err != nil {
		return nil, nil, fmt.Errorf("exepipe CopyCmd: JSON marshaling failed: %v", err)
	}

	getFD := func(c net.Conn) (int, error) {
		tcpConn, ok := c.(*net.TCPConn)
		if !ok {
			return 0, fmt.Errorf("exepipe CopyCmd: net.Conn (type %T) is not net.TCPConn", c)
		}
		rawConn, err := tcpConn.SyscallConn()
		if err != nil {
			return 0, fmt.Errorf("exepipe CopyCmd: SyscallConn failed: %w", err)
		}
		var fdi int
		err = rawConn.Control(func(fdu uintptr) {
			fdi = int(fdu)
		})
		if err != nil {
			return 0, fmt.Errorf("exepipe CopyCmd: rawConn.Control failed: %w", err)
		}
		return fdi, nil
	}

	fd1, err := getFD(c1)
	if err != nil {
		return nil, nil, err
	}
	fd2, err := getFD(c2)
	if err != nil {
		return nil, nil, err
	}

	oob = syscall.UnixRights(fd1, fd2)

	return data, oob, nil
}

// ListenCmd returns a marshalled command to listen on a
// network connection and connect it to a given host:port.
// Then it will copy between the two.
// The marshalled command includes both regular data and oob data,
// to be sent over a Unix socket.
// The caller must ensure that the listener is not closed
// until the data has been sent to exepipe.
// The typ argument is for metrics;
// it is expected to be something like "http" or "ssh".
func ListenCmd(listener net.Listener, dest, typ string) (data, oob []byte, err error) {
	c := cmd{
		Action: "listen",
		Arg:    dest,
		Type:   typ,
	}

	data, err = json.Marshal(c)
	if err != nil {
		return nil, nil, fmt.Errorf("exepipe ListenCmd: JSON marshaling failed: %v", err)
	}

	var fdListen int
	tcpListener, ok := listener.(*net.TCPListener)
	if !ok {
		return nil, nil, fmt.Errorf("exepipe ListenCmd: listener (type %T) is not net.TCPListener", listener)
	}
	rawConn, err := tcpListener.SyscallConn()
	if err != nil {
		return nil, nil, fmt.Errorf("exepipe ListenCmd: SyscallConn failed: %w", err)
	}
	err = rawConn.Control(func(fd uintptr) {
		fdListen = int(fd)
	})
	if err != nil {
		return nil, nil, fmt.Errorf("exepipe ListenCmd: listener.rawConn.Control failed: %w", err)
	}

	oob = syscall.UnixRights(fdListen)

	return data, oob, nil
}

// ErrDispatchFailed is returned by Dispatch if some error occurred.
// The log will show the error.
var ErrDispatchFailed = errors.New("exepipe command failure")

// Action is a function to call for a particular command.
// We don't bother returning errors from an action,
// it should just log them.
type Action func(ctx context.Context, fds []int, arg, typ string) error

// Actions maps from a command to the action.
type Actions map[string]Action

// Dispatch takes a message with regular and oob data
// and dispatches via an Actions map.
func Dispatch(ctx context.Context, lg *slog.Logger, actions Actions, data, oob []byte) error {
	var c cmd
	if err := json.Unmarshal(data, &c); err != nil {
		lg.ErrorContext(ctx, "failed to unmarshal exepipe command", "data", string(data), "error", err)
		return ErrDispatchFailed
	}
	action, ok := actions[c.Action]
	if !ok {
		lg.ErrorContext(ctx, "unrecognized exepipe command", "cmd", c.Action)
		return ErrDispatchFailed
	}

	var fds []int
	if len(oob) > 0 {
		scms, err := syscall.ParseSocketControlMessage(oob)
		if err != nil {
			lg.ErrorContext(ctx, "failed to parse exepipe file descriptors", "oob", string(oob), "error", err)
			return ErrDispatchFailed
		}
		if len(scms) != 1 {
			lg.ErrorContext(ctx, "exepipe saw wrong number of socket control messages", "count", len(scms))
			return ErrDispatchFailed
		}
		fds, err = syscall.ParseUnixRights(&scms[0])
		if err != nil {
			lg.ErrorContext(ctx, "failed to extract exepipe file descriptors", "oob", string(oob), "error", err)
			return ErrDispatchFailed
		}
	}

	return action(ctx, fds, c.Arg, c.Type)
}
