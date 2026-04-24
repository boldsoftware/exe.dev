//go:build linux

package exepipe

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"runtime"
	"strconv"
	"time"

	"github.com/vishvananda/netns"
)

// dial enters the named network namespace, if specified,
// before opening a connection to host/port.
// The TCP connection is established from within the netns;
// once connected, the socket works from any thread.
func dialNetns(ctx context.Context, lg *slog.Logger, nsName, host string, port int, timeout time.Duration) (net.Conn, error) {
	if nsName != "" {
		// LockOSThread so the netns switch only affects this goroutine.
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()

		origNS, err := netns.Get()
		if err != nil {
			return nil, fmt.Errorf("get current netns: %w", err)
		}
		defer origNS.Close()

		targetNS, err := netns.GetFromName(nsName)
		if err != nil {
			return nil, fmt.Errorf("get netns %s: %w", nsName, err)
		}
		defer targetNS.Close()

		if err := netns.Set(targetNS); err != nil {
			return nil, fmt.Errorf("enter netns %s: %w", nsName, err)
		}
		defer func() {
			if err := netns.Set(origNS); err != nil {
				lg.ErrorContext(ctx, "failed to restore original network namespace")
			}
		}()
	}

	d := net.Dialer{Timeout: timeout}
	return d.DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
}
