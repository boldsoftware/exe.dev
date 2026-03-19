//go:build !linux

package main

import (
	"fmt"
	"net"
	"runtime"
	"time"
)

func getSocketRTT(conn net.Conn) (time.Duration, error) {
	return 0, fmt.Errorf("TCP RTT not supported on %s", runtime.GOOS)
}
