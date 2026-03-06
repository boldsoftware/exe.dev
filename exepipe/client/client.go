// Package client provides functions to send commands to exepipe.
package client

import (
	"context"
	"fmt"
	"net"

	"exe.dev/exepipe/internal/cmds"
)

// Client is used to talk to exepipe.
type Client struct {
	addr *net.UnixAddr
	uc   *net.UnixConn
}

// NewClient opens a client talking to exepipe at the given address.
func NewClient(ctx context.Context, addr string) (*Client, error) {
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

	n, oobn, err := c.uc.WriteMsgUnix(data, oob, nil)
	if err != nil {
		return fmt.Errorf("exepipe Client.Copy error sending to exepipe: %w", err)
	}

	if n != len(data) || oobn != len(oob) {
		return fmt.Errorf("exepipe Client.Copy short: wrote %d, %d out of %d, %d", n, oobn, len(data), len(oob))
	}

	// At this point the file descriptors have been handed to the kernel
	// and it is safe to close the network connections.
	f1.Close()
	f2.Close()

	return nil
}

// Listen tells exepipe to listen on a socket.
// When any connection arrives, exepipe will open a TCP connection
// to the specified network destination, and copy data between
// the two connections.
// The network destination is "host:port".
// The typ argument is purely descriptive, something like "http" or "ssh".
// On success, this command will take ownership of the listener.
// This will return an error if there is some problem contacting exepipe.
// Errors while listening or copying will be logged by exepipe
// and will not be returned to the caller.
func (c *Client) Listen(ctx context.Context, listener net.Listener, dest, typ string) error {
	data, oob, err := cmds.ListenCmd(listener, dest, typ)
	if err != nil {
		return err
	}

	n, oobn, err := c.uc.WriteMsgUnix(data, oob, nil)
	if err != nil {
		return fmt.Errorf("exepipe Client.Listen error sending to exepipe: %w", err)
	}

	if n != len(data) || oobn != len(oob) {
		return fmt.Errorf("exepipe Client.Listen short: wrote %d, %d out of %d, %d", n, oobn, len(data), len(oob))
	}

	// At this point the file descriptor has been handed to the kernel
	// and it is safe to close the network connections.
	listener.Close()

	return nil
}
