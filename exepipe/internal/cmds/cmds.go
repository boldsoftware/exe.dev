// Package cmds defines the commands passed to exepipe.
// Commands are JSON encoded data, and may include file descriptors
// passed as ancillary data.
package cmds

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"log/slog"
	"net"
	"syscall"
)

// cmd is a single command sent to exepipe.
type cmd struct {
	Action string `json:"action"`         // what to do
	Key    string `json:"key,omitempty"`  // for lookups
	Host   string `json:"host,omitempty"` // connection host
	Port   int    `json:"port,omitempty"` // connection port
	Type   string `json:"type,omitempty"` // connection type
}

// response is a response to a copy or listen command.
type response struct {
	Ack string `json:"ack"` // acknowledgement
}

// CopyCmd returns a marshalled command to copy
// data from one network connection to another
//
// This is called by an exepipe client when sending a copy command.
//
// The marshalled command includes both regular data and oob data,
// to be sent over a Unix socket.
// The caller must ensure that the network connections
// are not closed until the data has been sent to exepipe.
//
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
//
// This is called by an exepipe client when sending a listen command.
//
// The marshalled command includes both regular data and oob data,
// to be sent over a Unix socket.
// The caller must ensure that the listener is not closed
// until the data has been sent to exepipe.
//
// The typ argument is for metrics;
// it is expected to be something like "http" or "ssh".
func ListenCmd(key string, listener net.Listener, host string, port int, typ string) (data, oob []byte, err error) {
	c := cmd{
		Action: "listen",
		Key:    key,
		Host:   host,
		Port:   port,
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

// ListenersCmd returns a marshalled command to fetch all listeners.
//
// This is called by an exepipe client.
//
// This command does not use oob data.
func ListenersCmd() (data []byte, err error) {
	c := cmd{
		Action: "listeners",
	}
	data, err = json.Marshal(c)
	if err != nil {
		return nil, fmt.Errorf("exepipe ListenersCmd: JSON marshaling failed: %v", err)
	}

	return data, nil
}

// ErrDispatchFailed is returned by Dispatch if some error occurred.
// The log will show the error.
var ErrDispatchFailed = errors.New("exepipe command failure")

// Action is a function to call for a particular command.
// We don't bother returning errors from an action,
// it should just log them.
type Action func(ctx context.Context, key string, fds []int, host string, port int, typ string) error

// Actions maps from a command to the action.
type Actions map[string]Action

// Dispatch takes a message with regular and oob data
// and dispatches via an Actions map.
// This is called by the exepipe server when it receives a command.
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

	return action(ctx, c.Key, fds, c.Host, c.Port, c.Type)
}

// MarshalResponse returns a marshalled response to a command.
// The empty string indicates succeed.
// This is called by the exepipe server to respond to a command.
func MarshalResponse(ctx context.Context, lg *slog.Logger, ack string) ([]byte, error) {
	r := response{
		Ack: ack,
	}

	data, err := json.Marshal(r)
	if err != nil {
		return nil, fmt.Errorf("exepipe MarshalResponse: JSON marshaling failed: %v", err)
	}

	return data, err
}

// UnmarshalResponse parses the response from a command.
// This response is the empty string on succeed,
// an error message on failure.
//
// This is called by an exepipe client to decode the server's response.
func UnmarshalResponse(ctx context.Context, lg *slog.Logger, data, oob []byte) (string, error) {
	if len(oob) > 0 {
		lg.ErrorContext(ctx, "unexpected oob in exepipe response", "data", string(data), "oob", string(oob))
		return "", errors.New("unexpected oob in exepipe response")
	}

	var r response
	if err := json.Unmarshal(data, &r); err != nil {
		lg.ErrorContext(ctx, "failed to unmarshal exepipe response", "data", string(data), "error", err)
		return "", err
	}

	return r.Ack, nil
}

// Listener describes a single listener.
type Listener struct {
	Key  string `json:"key"`
	Host string `json:"host"`
	Port int    `json:"port"`
	Type string `json:"type"`
}

// MarshalListenersResponse returns a stream of marshalled responses
// to the listeners command describing a list of listeners.
// This is called by the exepipe server.
//
// This returns a series of packets, so that any single packet
// is not too long. The packets do not use oob.
// The stream ends with a zero Listener.
func MarshalListenersResponse(ctx context.Context, lg *slog.Logger, listeners iter.Seq[Listener], perr *error) iter.Seq[[]byte] {
	// We are sending packets over a seqpacket Unix stream.
	// The maximum packet size is typically 208K.
	// To give plenty of room, we send 200 listeners per packet.
	const limit = 200

	return func(yield func([]byte) bool) {
		var send func(s []Listener) bool
		send = func(s []Listener) bool {
			data, err := json.Marshal(s)
			if err != nil {
				*perr = err
				if len(s) == 1 && s[0] == (Listener{}) {
					// We somehow failed to send
					// the packet saying there is no
					// more data. We are stuck.
					lg.ErrorContext(ctx, "exepipe MarshalListenersResponse failed to marshal termination packet", "error", err)
					return false
				}
				lg.ErrorContext(ctx, "exepipe MarshalListenersResponse marshalling error", "error", err)
				// In order to not break the communication,
				// we need to send an empty packet.
				s = []Listener{Listener{}}
				send(s)
				return false
			}
			return yield(data)
		}

		var s []Listener
		for ln := range listeners {
			if len(s) == limit {
				if !send(s) {
					return
				}
				s = s[:0]
			}

			s = append(s, ln)
		}

		s = append(s, Listener{})
		send(s)
	}
}

// UnmarshalListenersResponse calls the yield function with
// a stream of listeners from a packet sent by the listeners command.
// This doesn't return an iterator, it is called by an iterator.
//
// If yield is nil, this just parses and discards the packet;
// this is so that we discard packets if the caller doesn't want them all.
// We return the possibly-nil yield function, so that the caller can
// pass it back to us if there are more packets.
//
// It reports whether another packet is expected.
func UnmarshalListenersResponse(ctx context.Context, lg *slog.Logger, yield func(Listener, error) bool, data []byte) (bool, func(Listener, error) bool) {
	var s []Listener
	if err := json.Unmarshal(data, &s); err != nil {
		lg.ErrorContext(ctx, "failed to unmarshal exepipe listeners response", "data", string(data), "error", err)
		if yield != nil {
			yield(Listener{}, err)
		}
		return false, nil
	}

	more := true
	if len(s) > 0 {
		last := len(s) - 1
		if s[last] == (Listener{}) {
			more = false
			s = s[:last]
		}
	}

	if yield != nil {
		for _, ln := range s {
			if !yield(ln, nil) {
				yield = nil
				break
			}
		}
	}

	return more, yield
}
