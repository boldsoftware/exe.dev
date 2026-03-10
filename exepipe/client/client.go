// Package client provides functions to send commands to exepipe.
package client

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"log/slog"
	"net"
	"sync"

	"exe.dev/exepipe/internal/cmds"
)

// Client is used to talk to exepipe.
type Client struct {
	addr *net.UnixAddr
	uc   *net.UnixConn
	lg   *slog.Logger
	mu   sync.Mutex
}

// NewClient opens a client talking to exepipe at the given address.
func NewClient(ctx context.Context, addr string, lg *slog.Logger) (*Client, error) {
	unixAddr := &net.UnixAddr{
		Name: addr,
		Net:  "unixpacket",
	}
	var d net.Dialer
	uc, err := d.DialUnix(ctx, "unixpacket", nil, unixAddr)
	if err != nil {
		return nil, err
	}
	c := &Client{
		addr: unixAddr,
		uc:   uc,
		lg:   lg,
	}
	return c, nil
}

// Copy tells exepipe to copy data between two network connections.
// The typ argument is purely description, something like "http" or "ssh".
// On success, this command will take ownership of the connections
// and close them when the copying is complete.
// This will return an error if there is some problem contacting exepipe.
// Errors during copying will be logged by exepipe and will not be
// returned to the caller.
func (c *Client) Copy(ctx context.Context, f1, f2 net.Conn, typ string) error {
	data, oob, err := cmds.CopyCmd(f1, f2, typ)
	if err != nil {
		return err
	}

	if err = c.sendCmd(ctx, data, oob); err != nil {
		return err
	}

	// At this point the file descriptors have been handed to exepipe
	// and it is safe to close the network connections.
	f1.Close()
	f2.Close()

	return nil
}

// Listen tells exepipe to listen on a socket.
// When any connection arrives, exepipe will open a TCP connection
// to the specified network destination, and copy data between
// the two connections.
// The key may be used to stop the listener, and is returned by Listeners.
// The network destination is host:port
// The typ argument is purely descriptive, something like "http" or "ssh".
// On success, this command will take ownership of the listener.
// This will return an error if there is some problem contacting exepipe.
// Errors while listening or copying will be logged by exepipe
// and will not be returned to the caller.
func (c *Client) Listen(ctx context.Context, key string, listener net.Listener, host string, port int, typ string) error {
	data, oob, err := cmds.ListenCmd(key, listener, host, port, typ)
	if err != nil {
		return err
	}

	if err = c.sendCmd(ctx, data, oob); err != nil {
		return err
	}

	// At this point the file descriptor has been handed to exepipe
	// and it is safe to close the network connections.
	listener.Close()

	return nil
}

// Unlisten tells exepipe to close an existing listener.
// This does not affect any existing network connections that
// started from that listener.
func (c *Client) Unlisten(ctx context.Context, key string) error {
	data, err := cmds.UnlistenCmd(key)
	if err != nil {
		return err
	}

	return c.sendCmd(ctx, data, nil)
}

// sendCmd sends a comment to exepipe and waits for an ack.
func (c *Client) sendCmd(ctx context.Context, data, oob []byte) error {
	// Lock so that concurrent calls don't mix up responses.
	c.mu.Lock()
	defer c.mu.Unlock()

	n, oobn, err := c.uc.WriteMsgUnix(data, oob, nil)
	if err != nil {
		return fmt.Errorf("error sending to exepipe: %w", err)
	}

	if n != len(data) || oobn != len(oob) {
		return fmt.Errorf("exepipe client short write: wrote %d, %d out of %d, %d", n, oobn, len(data), len(oob))
	}

	return c.readResponse(ctx)
}

// readResponse reads the response to a command.
func (c *Client) readResponse(ctx context.Context) error {
	var rdata, roob [512]byte
	n, oobn, _, _, err := c.uc.ReadMsgUnix(rdata[:], roob[:])
	if err != nil {
		return fmt.Errorf("error receiving from exepipe: %w", err)
	}

	ack, err := cmds.UnmarshalResponse(ctx, c.lg, rdata[:n], roob[:oobn])
	if err != nil {
		return err
	}

	if len(ack) > 0 {
		return errors.New(ack)
	}

	return nil
}

// Listener describes an exepipe listener.
// These are taken from the values passed to [Client.Listen].
type Listener struct {
	Key  string // key
	Host string // connection host
	Port int    // connection port
	Type string // type
}

// Listeners asks exepipe for all current listeners.
func (c *Client) Listeners(ctx context.Context) iter.Seq2[Listener, error] {
	return func(yield func(Listener, error) bool) {
		for ln, err := range c.listeners(ctx) {
			cln := Listener{
				Key:  ln.Key,
				Host: ln.Host,
				Port: ln.Port,
				Type: ln.Type,
			}
			if !yield(cln, err) {
				break
			}
		}
	}
}

// listeners returns the current listeners as cmds.Listener values.
func (c *Client) listeners(ctx context.Context) iter.Seq2[cmds.Listener, error] {
	return func(yield func(cmds.Listener, error) bool) {
		c.mu.Lock()
		defer c.mu.Unlock()

		data, err := cmds.ListenersCmd()
		if err != nil {
			yield(cmds.Listener{}, err)
			return
		}

		n, oobn, err := c.uc.WriteMsgUnix(data, nil, nil)
		if err != nil {
			yield(cmds.Listener{}, fmt.Errorf("error sending to exepipe: %v", err))
			return
		}

		if n != len(data) || oobn != 0 {
			yield(cmds.Listener{}, fmt.Errorf("exepipe client short write: wrote %d, %d out of %d, %d", n, oobn, len(data), 0))
			return
		}

		// maxPacketSize is a typical Linux default.
		const maxPacketSize = 208 * 1024

		rdata := make([]byte, maxPacketSize)
		roob := make([]byte, 512)
		more := true
		for more {
			n, oobn, _, _, err := c.uc.ReadMsgUnix(rdata, roob)
			if err != nil {
				yield(cmds.Listener{}, err)
				return
			}
			if oobn > 0 {
				yield(cmds.Listener{}, errors.New("unexpected oob in listeners"))
				return
			}

			more, yield = cmds.UnmarshalListenersResponse(ctx, c.lg, yield, rdata[:n])
		}

		// After the listeners comes a final ack.
		err = c.readResponse(ctx)
		if err != nil {
			if yield != nil {
				yield(cmds.Listener{}, err)
			}
		}
	}
}
