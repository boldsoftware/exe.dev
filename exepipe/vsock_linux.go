//go:build linux

package exepipe

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"syscall"

	"github.com/mdlayher/vsock"
)

// connectVSock opens a connection to the vsock destination host:port,
// and starts copying from conn.
// This runs in a separate goroutine.
func (p *piping) connectVSock(ctx context.Context, conn1 net.Conn, key string, hostNum, port int, typ string) {
	conn2, err := vsock.Dial(uint32(hostNum), uint32(port), nil)
	if err != nil {
		conn1.Close()
		level := slog.LevelError
		switch {
		case errors.Is(err, syscall.ECONNREFUSED),
			errors.Is(err, syscall.EHOSTUNREACH):
			level = slog.LevelWarn
		}

		p.pipeInstance.lg.Log(ctx, level, "exepipe failed to connect to vsock", "key", key, "host", hostNum, "port", port, "type", typ, "error", err)
		return
	}

	p.copyConns(ctx, conn1, conn2, typ)
}
