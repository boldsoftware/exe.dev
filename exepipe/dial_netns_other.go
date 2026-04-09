//go:build !linux

package exepipe

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"time"
)

// NetnsDialFunc returns a DialFunc that ignores the netns argument
// on non-Linux platforms (netns is not supported).
func NetnsDialFunc() DialFunc {
	return func(_ context.Context, host string, port int, nsName string, timeout time.Duration) (net.Conn, error) {
		if nsName != "" {
			return nil, fmt.Errorf("network namespaces not supported on this platform")
		}
		d := net.Dialer{Timeout: timeout}
		return d.Dial("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	}
}
