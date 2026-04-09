//go:build linux

package exepipe

import (
	"context"
	"fmt"
	"net"
	"runtime"
	"strconv"
	"time"

	"github.com/vishvananda/netns"
)

// NetnsDialFunc returns a DialFunc that enters the named network
// namespace before dialing. The TCP connection is established from
// within the netns; once connected, the socket works from any thread.
func NetnsDialFunc() DialFunc {
	return func(ctx context.Context, host string, port int, nsName string, timeout time.Duration) (net.Conn, error) {
		if nsName == "" {
			d := net.Dialer{Timeout: timeout}
			return d.DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
		}

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
		defer netns.Set(origNS) //nolint:errcheck

		d := net.Dialer{Timeout: timeout}
		return d.DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	}
}
