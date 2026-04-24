package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
)

const tailscaledSocket = "/var/run/tailscale/tailscaled.sock"

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

// TailscaleWhoIs queries the Tailscale local API to identify the peer at
// the given remoteAddr (ip:port as returned by http.Request.RemoteAddr).
func TailscaleWhoIs(ctx context.Context, remoteAddr string) (Whois, error) {
	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", tailscaledSocket)
			},
		},
	}

	req, err := http.NewRequestWithContext(ctx, "GET",
		"http://local-tailscaled.sock/localapi/v0/whois?addr="+remoteAddr, nil)
	if err != nil {
		return Whois{}, fmt.Errorf("tailscale whois: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return Whois{}, fmt.Errorf("tailscale whois: %w", err)
	}
	defer resp.Body.Close()

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
