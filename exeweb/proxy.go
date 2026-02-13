package exeweb

import (
	"log/slog"
	"net"
	"net/url"
)

// BoxSSHHost returns the SSH host to use to connect to a box on ctrHost.
func BoxSSHHost(lg *slog.Logger, ctrHost string) string {
	// TODO: maybe this is a url and needs to be stripped.
	// ctrHost is usually tcp://host:port/ of the exelet.
	// The VMs SSH server is mapped to a port (box.sshPort) on the same
	// host as the exelet, so we parse out the host part only.
	if u, err := url.Parse(ctrHost); err == nil && u.Host != "" {
		if host, _, err := net.SplitHostPort(u.Host); err == nil {
			return host
		} else {
			return u.Host
		}
	}
	// This should never happen, but since we're dealing with data
	// from the database, let's avoid crashing for now.
	lg.Error("Box Ctrhost is not a valid URL", "ctrhost", ctrHost)
	return ctrHost
}
