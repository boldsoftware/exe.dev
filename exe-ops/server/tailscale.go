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

// tailscaleWhoisResponse is the subset of the Tailscale whois response we use.
type tailscaleWhoisResponse struct {
	UserProfile struct {
		LoginName   string `json:"LoginName"`
		DisplayName string `json:"DisplayName"`
	} `json:"UserProfile"`
	Node struct {
		ComputedName string `json:"ComputedName"`
	} `json:"Node"`
}

// TailscaleWhoIs queries the Tailscale local API to identify the peer at
// the given remoteAddr (ip:port as returned by http.Request.RemoteAddr).
// It returns the user's login name (e.g. "philip@example.com") or display
// name, or an error if the lookup fails.
func TailscaleWhoIs(ctx context.Context, remoteAddr string) (string, error) {
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
		return "", fmt.Errorf("tailscale whois: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("tailscale whois: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("tailscale whois %s: %s", resp.Status, body)
	}

	var whois tailscaleWhoisResponse
	if err := json.NewDecoder(resp.Body).Decode(&whois); err != nil {
		return "", fmt.Errorf("tailscale whois decode: %w", err)
	}

	if whois.UserProfile.LoginName != "" {
		return whois.UserProfile.LoginName, nil
	}
	if whois.UserProfile.DisplayName != "" {
		return whois.UserProfile.DisplayName, nil
	}
	if whois.Node.ComputedName != "" {
		return whois.Node.ComputedName, nil
	}
	return "", fmt.Errorf("tailscale whois: no identity found for %s", remoteAddr)
}
