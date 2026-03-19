//go:build !linux

package tcprtt

import (
	"fmt"
	"net"
	"runtime"
	"time"
)

// Get returns the smoothed RTT of the TCP connection underlying conn.
// On non-Linux platforms this always returns an error.
func Get(conn net.Conn) (time.Duration, error) {
	return 0, fmt.Errorf("TCP RTT not supported on %s", runtime.GOOS)
}
