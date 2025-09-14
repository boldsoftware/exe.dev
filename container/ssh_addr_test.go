package container

import (
	"fmt"
	"net"
	"strings"

	"exe.dev/ctrhosttest"
)

// sshDialAddr returns the correct tcp address for dialing SSH into the container.
// If the container runs on a remote ctr-host, use that host's IP/name; otherwise fallback to localhost.
func sshDialAddr(c *Container) string {
	host := "localhost"
	if c.DockerHost != "" {
		h := strings.TrimPrefix(c.DockerHost, "ssh://")
		if at := strings.LastIndex(h, "@"); at != -1 {
			h = h[at+1:]
		}
		if h != "" {
			host = h
		}
	}
	// Resolve SSH config alias (e.g., lima-exe-ctr-tests) to a concrete host/IP for TCP dialing.
	if res := ctrhosttest.ResolveHostFromSSHConfig(host); res != "" {
		host = res
	}
	return net.JoinHostPort(host, fmt.Sprint(c.SSHPort))
}
