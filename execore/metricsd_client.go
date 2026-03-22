package execore

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"exe.dev/metricsd/types"
)

// metricsClient queries the metricsd server for VM metrics.
type metricsClient struct {
	baseURL    string
	httpClient *http.Client
}

func newMetricsClient(baseURL string) *metricsClient {
	return &metricsClient{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// queryVMs fetches metrics for the given VM names over the specified number of hours.
func (c *metricsClient) queryVMs(ctx context.Context, vmNames []string, hours int) (map[string][]types.Metric, error) {
	reqBody := types.QueryVMsRequest{
		VMNames: vmNames,
		Hours:   hours,
	}
	data, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/query/vms", bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("query metricsd: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("metricsd returned status %d", resp.StatusCode)
	}

	var result types.QueryVMsResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return result.VMs, nil
}
