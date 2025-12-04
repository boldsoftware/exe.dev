// Package alley53 provides a client for the alley53 local DNS server.
package alley53

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"time"
)

// Client is an HTTP client for the alley53 DNS server.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// NewClient creates a new alley53 client.
// The addr should be the alley53 HTTP API address (e.g., "localhost:5380").
func NewClient(addr string) *Client {
	return &Client{
		baseURL: "http://" + addr,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// UpsertBoxRecord creates or updates an A record for a box.
// The shard maps to an IP like 127.21.0.1, 127.21.0.2, etc.
func (c *Client) UpsertBoxRecord(ctx context.Context, domain, boxName string, shard int) error {
	ip := net.IPv4(127, 21, 0, byte(shard))

	body, err := json.Marshal(map[string]string{
		"name": boxName,
		"ip":   ip.String(),
	})
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/upsert", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to upsert record: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("alley53 returned status %d", resp.StatusCode)
	}

	return nil
}

// DeleteBoxRecord removes the A record for a box.
func (c *Client) DeleteBoxRecord(ctx context.Context, domain, boxName string, shard int) error {
	req, err := http.NewRequestWithContext(ctx, "DELETE", c.baseURL+"/record?name="+boxName, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to delete record: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("alley53 returned status %d", resp.StatusCode)
	}

	return nil
}

// IsRunning reports whether alley53 is running by hitting the health endpoint.
func (c *Client) IsRunning(ctx context.Context) bool {
	req, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+"/health", nil)
	if err != nil {
		return false
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	return resp.StatusCode == http.StatusOK
}
