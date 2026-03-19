package tcprtt

import (
	"crypto/tls"
	"fmt"
	"net"
	"time"

	"golang.org/x/sys/unix"
)

// Get returns the smoothed RTT of the TCP connection underlying conn.
// It works with plain *net.TCPConn and *tls.Conn wrapping a *net.TCPConn.
// Returns 0 and an error if RTT cannot be determined.
func Get(conn net.Conn) (time.Duration, error) {
	rawConn := unwrapTCP(conn)
	if rawConn == nil {
		return 0, fmt.Errorf("not a TCP connection: %T", conn)
	}

	sc, err := rawConn.SyscallConn()
	if err != nil {
		return 0, fmt.Errorf("SyscallConn: %w", err)
	}

	var rtt time.Duration
	var sysErr error
	err = sc.Control(func(fd uintptr) {
		info, err := unix.GetsockoptTCPInfo(int(fd), unix.IPPROTO_TCP, unix.TCP_INFO)
		if err != nil {
			sysErr = err
			return
		}
		rtt = time.Duration(info.Rtt) * time.Microsecond
	})
	if err != nil {
		return 0, fmt.Errorf("Control: %w", err)
	}
	if sysErr != nil {
		return 0, fmt.Errorf("getsockopt TCP_INFO: %w", sysErr)
	}
	return rtt, nil
}

// unwrapTCP extracts the underlying *net.TCPConn from conn.
func unwrapTCP(conn net.Conn) *net.TCPConn {
	switch c := conn.(type) {
	case *net.TCPConn:
		return c
	case *tls.Conn:
		return unwrapTCP(c.NetConn())
	default:
		// Try the NetConn() interface for other wrappers.
		type netConner interface {
			NetConn() net.Conn
		}
		if nc, ok := conn.(netConner); ok {
			return unwrapTCP(nc.NetConn())
		}
		return nil
	}
}
