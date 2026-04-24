//go:build !linux

package exepipe

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"time"
)

// dial opens a connection to host/port.
// This is the non-Linux version that issues an error
// if a namespace is specified.
func dialNetns(ctx context.Context, lg *slog.Logger, nsName, host string, port int, timeout time.Duration) (net.Conn, error) {
	if nsName != "" {
		return nil, fmt.Errorf("network namespaces not supported on this platform")
	}

	d := net.Dialer{Timeout: timeout}
	return d.Dial("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
}
