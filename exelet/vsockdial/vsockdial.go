// Package vsockdial connects to per-VM AF_VSOCK servers (memd, op-ssh)
// exposed by cloud-hypervisor as a hybrid-vsock unix-domain socket on the
// host.
//
// CH binds one unix socket per VM. Reaching the in-guest service is two
// steps:
//
//  1. Open the unix socket.
//  2. Send the literal line "CONNECT <port>\n" and read one line back; CH
//     replies with "OK <gid>\n" on success and any other text on failure.
//
// The returned net.Conn carries bytes directly between the caller and the
// in-guest server on the requested vsock port.
package vsockdial

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"
)

// Well-known vsock ports for in-guest services. Mirrored in
// cmd/exe-init/vsock_ports.go for the guest side.
const (
	OperatorSSHVsockPort uint32 = 2222
	MemdVsockPort        uint32 = 2223
)

// DefaultHandshakeTimeout bounds the CONNECT handshake; the unix socket is
// always local but cloud-hypervisor's accept loop can briefly block.
const DefaultHandshakeTimeout = 5 * time.Second

// SocketPath returns the cloud-hypervisor hybrid-vsock unix socket path
// for a given VM instance under the exelet data dir. Mirrors VMM
// runtime layout (`<dataDir>/runtime/<instanceID>/opssh.sock`).
func SocketPath(dataDir, instanceID string) string {
	return dataDir + "/runtime/" + instanceID + "/opssh.sock"
}

// Dial opens the unix socket at socketPath and performs the CH hybrid-vsock
// CONNECT handshake to the given port. The returned Conn is positioned
// immediately past the "OK ...\n" reply.
func Dial(ctx context.Context, socketPath string, port uint32) (net.Conn, error) {
	return DialTimeout(ctx, socketPath, port, DefaultHandshakeTimeout)
}

// DialTimeout is Dial with an explicit handshake timeout.
func DialTimeout(ctx context.Context, socketPath string, port uint32, handshakeTimeout time.Duration) (net.Conn, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("dial unix %s: %w", socketPath, err)
	}
	deadline := time.Now().Add(handshakeTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)
	if _, err := fmt.Fprintf(conn, "CONNECT %d\n", port); err != nil {
		conn.Close()
		return nil, fmt.Errorf("write CONNECT: %w", err)
	}
	br := bufio.NewReader(conn)
	line, err := br.ReadString('\n')
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("read CONNECT reply: %w", err)
	}
	line = strings.TrimRight(line, "\r\n")
	if !strings.HasPrefix(line, "OK") {
		conn.Close()
		return nil, fmt.Errorf("%w: %q", ErrUnexpectedReply, line)
	}
	_ = conn.SetDeadline(time.Time{})
	// Hand back a wrapped conn so any bytes already buffered by br are not
	// lost (in practice the OK reply ends with a newline and br is empty,
	// but be defensive).
	return &bufConn{Conn: conn, br: br}, nil
}

// ErrUnexpectedReply is returned for non-OK CH replies. Wrapped errors that
// callers want to switch on can use errors.Is.
var ErrUnexpectedReply = errors.New("vsockdial: unexpected CH reply")

type bufConn struct {
	net.Conn
	br *bufio.Reader
}

func (c *bufConn) Read(p []byte) (int, error) { return c.br.Read(p) }

// StdioConn adapts a process stdio pair (e.g. socat over SSH) into a
// net.Conn so it can be reused by callers expecting a real connection.
// Used by e1e tests that tunnel via socat across hosts.
type StdioConn struct {
	R         io.Reader
	W         io.WriteCloser
	CloseFunc func()
	once      sync.Once
}

func (c *StdioConn) Read(p []byte) (int, error)  { return c.R.Read(p) }
func (c *StdioConn) Write(p []byte) (int, error) { return c.W.Write(p) }
func (c *StdioConn) Close() error {
	c.once.Do(func() {
		if c.CloseFunc != nil {
			c.CloseFunc()
		}
	})
	return nil
}

type stdioAddr struct{}

func (stdioAddr) Network() string { return "vsock" }
func (stdioAddr) String() string  { return "vsock" }

func (c *StdioConn) LocalAddr() net.Addr              { return stdioAddr{} }
func (c *StdioConn) RemoteAddr() net.Addr             { return stdioAddr{} }
func (c *StdioConn) SetDeadline(time.Time) error      { return nil }
func (c *StdioConn) SetReadDeadline(time.Time) error  { return nil }
func (c *StdioConn) SetWriteDeadline(time.Time) error { return nil }
