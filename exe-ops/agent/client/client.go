package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"exe.dev/exe-ops/apitype"
)

// ReportResponse contains the result of sending a report.
type ReportResponse struct {
	UpgradeAvailable bool
}

// Client sends reports to the exe-ops server.
type Client struct {
	serverURL  string
	token      string
	httpClient *http.Client
}

// New creates a new Client.
func New(serverURL, token string) *Client {
	return &Client{
		serverURL: serverURL,
		token:     token,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// SendReport sends a report to the server and returns the response.
func (c *Client) SendReport(ctx context.Context, report *apitype.Report) (*ReportResponse, error) {
	body, err := json.Marshal(report)
	if err != nil {
		return nil, fmt.Errorf("marshal report: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.serverURL+"/api/v1/report", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send report: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		return nil, fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}

	result := &ReportResponse{
		UpgradeAvailable: resp.Header.Get("X-Upgrade-Available") == "true",
	}
	return result, nil
}

// StreamConnect opens a long-lived SSE connection to the server for presence detection.
// The caller owns the returned response body and must close it.
func (c *Client) StreamConnect(ctx context.Context, name string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.serverURL+"/api/v1/stream?name="+name, nil)
	if err != nil {
		return nil, fmt.Errorf("create stream request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)

	// Use a separate client without timeout for long-lived SSE connection.
	streamClient := &http.Client{}
	resp, err := streamClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("stream connect: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("stream connect: HTTP %d", resp.StatusCode)
	}
	return resp, nil
}

// DownloadAgentBinary downloads the agent binary for the given OS/arch from the server.
func (c *Client) DownloadAgentBinary(ctx context.Context, name, goos, goarch string) ([]byte, string, error) {
	url := c.serverURL + "/api/v1/agent/binary?name=" + name + "&os=" + goos + "&arch=" + goarch
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("download agent binary: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("download failed: HTTP %d", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", fmt.Errorf("read binary: %w", err)
	}

	newVersion := resp.Header.Get("X-Agent-Version")
	return data, newVersion, nil
}
