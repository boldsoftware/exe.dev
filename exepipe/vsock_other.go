//go:build !linux

package exepipe

import (
	"context"
	"net"
	"runtime"
)

// connectVSock opens a connection to the vsock destination host:port,
// and starts copying from conn.
// This runs in a separate goroutine.
func (p *piping) connectVSock(ctx context.Context, conn1 net.Conn, key string, hostNum, port int, typ string) {
	// We should never get here. This is fail-safe code.
	conn1.Close()
	p.pipeInstance.lg.ErrorContext(ctx, "attempt to use vsock on non-Linux platform", "GOOS", runtime.GOOS, "key", key, "host", hostNum, "port", port, "type", typ)
}
