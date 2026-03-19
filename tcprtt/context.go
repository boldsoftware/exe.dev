package tcprtt

import (
	"context"
	"net"
)

type connKeyType struct{}

var connKey connKeyType

// ContextWithConn returns a new context with the given connection stored.
func ContextWithConn(ctx context.Context, conn net.Conn) context.Context {
	return context.WithValue(ctx, connKey, conn)
}

// ConnFromContext returns the connection stored in ctx, or nil.
func ConnFromContext(ctx context.Context) net.Conn {
	c, _ := ctx.Value(connKey).(net.Conn)
	return c
}
