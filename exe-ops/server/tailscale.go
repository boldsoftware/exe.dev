package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

const tailscaledSocket = "/var/run/tailscale/tailscaled.sock"

// tailscaledClient is shared across whois requests. Without a singleton
// every call leaked the underlying Transport's idle persistConn (and its
// readLoop+writeLoop goroutines) since the Transport's default
// IdleConnTimeout is 0. Auth middleware runs whois on every request, so
// the leak compounded with traffic.
var tailscaledClient = &http.Client{
	Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", tailscaledSocket)
		},
		MaxIdleConns:        4,
		MaxIdleConnsPerHost: 4,
		IdleConnTimeout:     90 * time.Second,
	},
}

// Whois identifies a Tailscale peer.
type Whois struct {
	LoginName    string
	DisplayName  string
	ComputedName string
	// Tags are any ACL tags assigned to the peer node. A non-empty Tags
	// slice means the peer is a tagged device (i.e. not a human user).
	Tags []string
}

// IsHuman reports whether the peer is a human Tailscale user, as opposed
// to a tagged device such as an agent, server, or CI runner.
func (w Whois) IsHuman() bool {
	return len(w.Tags) == 0 && w.LoginName != ""
}

// Identity returns a human-readable identifier for the peer, preferring
// LoginName, then DisplayName, then ComputedName. Suitable for logs and
// audit fields; not for authorization decisions (use IsHuman for that).
func (w Whois) Identity() string {
	switch {
	case w.LoginName != "":
		return w.LoginName
	case w.DisplayName != "":
		return w.DisplayName
	default:
		return w.ComputedName
	}
}

// tailscaleWhoisResponse is the subset of the Tailscale whois response we use.
type tailscaleWhoisResponse struct {
	UserProfile struct {
		LoginName   string `json:"LoginName"`
		DisplayName string `json:"DisplayName"`
	} `json:"UserProfile"`
	Node struct {
		ComputedName string   `json:"ComputedName"`
		Tags         []string `json:"Tags"`
	} `json:"Node"`
}

// TailscaleIPs returns this node's Tailscale IPs (typically one IPv4 and
// one IPv6) by querying the Tailscale local API. Used to bind listeners
// to the tailnet interface only, so the public network never sees the
// port.
func TailscaleIPs(ctx context.Context) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET",
		"http://local-tailscaled.sock/localapi/v0/status?peers=false", nil)
	if err != nil {
		return nil, fmt.Errorf("tailscale status: %w", err)
	}

	resp, err := tailscaledClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("tailscale status: %w", err)
	}
	defer func() {
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("tailscale status %s: %s", resp.Status, body)
	}

	var raw struct {
		Self struct {
			TailscaleIPs []string `json:"TailscaleIPs"`
		} `json:"Self"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("tailscale status decode: %w", err)
	}
	return raw.Self.TailscaleIPs, nil
}

// TailscaleWhoIs queries the Tailscale local API to identify the peer at
// the given remoteAddr (ip:port as returned by http.Request.RemoteAddr).
func TailscaleWhoIs(ctx context.Context, remoteAddr string) (Whois, error) {
	req, err := http.NewRequestWithContext(ctx, "GET",
		"http://local-tailscaled.sock/localapi/v0/whois?addr="+remoteAddr, nil)
	if err != nil {
		return Whois{}, fmt.Errorf("tailscale whois: %w", err)
	}

	resp, err := tailscaledClient.Do(req)
	if err != nil {
		return Whois{}, fmt.Errorf("tailscale whois: %w", err)
	}
	defer func() {
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return Whois{}, fmt.Errorf("tailscale whois %s: %s", resp.Status, body)
	}

	var raw tailscaleWhoisResponse
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return Whois{}, fmt.Errorf("tailscale whois decode: %w", err)
	}

	return Whois{
		LoginName:    raw.UserProfile.LoginName,
		DisplayName:  raw.UserProfile.DisplayName,
		ComputedName: raw.Node.ComputedName,
		Tags:         raw.Node.Tags,
	}, nil
}
