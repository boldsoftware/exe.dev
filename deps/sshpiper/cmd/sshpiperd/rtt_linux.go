package main

import (
	"fmt"
	"net"
	"time"

	"golang.org/x/sys/unix"
)

// getSocketRTT returns the smoothed RTT of a TCP connection.
func getSocketRTT(conn net.Conn) (time.Duration, error) {
	tcpConn, ok := conn.(*net.TCPConn)
	if !ok {
		return 0, fmt.Errorf("not a TCP connection: %T", conn)
	}

	sc, err := tcpConn.SyscallConn()
	if err != nil {
		return 0, err
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
		return 0, err
	}
	if sysErr != nil {
		return 0, sysErr
	}
	return rtt, nil
}
