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

// queryUsage fetches usage summaries for the given resource groups over the specified period.
func (c *metricsClient) queryUsage(ctx context.Context, resourceGroups []string, start, end time.Time) ([]types.UsageSummary, error) {
	reqBody := types.QueryUsageRequest{
		ResourceGroups: resourceGroups,
		Start:          start,
		End:            end,
	}
	data, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/query/usage", bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("query metricsd usage: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("metricsd returned status %d", resp.StatusCode)
	}

	var result types.QueryUsageResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return result.Metrics, nil
}

// queryDaily fetches daily metrics for the given resource groups over the specified period.
func (c *metricsClient) queryDaily(ctx context.Context, resourceGroups []string, start, end time.Time) ([]types.DailyMetric, error) {
	reqBody := types.QueryDailyRequest{
		ResourceGroups: resourceGroups,
		Start:          start,
		End:            end,
	}
	data, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/query/daily", bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("query metricsd daily: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("metricsd returned status %d", resp.StatusCode)
	}

	var result types.QueryDailyResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return result.Metrics, nil
}

// queryMonthly fetches monthly metrics for the given resource groups over the specified period.
func (c *metricsClient) queryMonthly(ctx context.Context, resourceGroups []string, start, end time.Time, groupByVM bool) ([]types.MonthlyMetric, error) {
	reqBody := types.QueryMonthlyRequest{
		ResourceGroups: resourceGroups,
		Start:          start,
		End:            end,
		GroupByVM:      groupByVM,
	}
	data, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/query/monthly", bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("query metricsd monthly: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("metricsd returned status %d", resp.StatusCode)
	}

	var result types.QueryMonthlyResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return result.Metrics, nil
}

// queryHourly fetches hourly metrics for the given resource groups over the specified period.
func (c *metricsClient) queryHourly(ctx context.Context, resourceGroups []string, start, end time.Time) ([]types.HourlyMetric, error) {
	reqBody := types.QueryHourlyRequest{
		ResourceGroups: resourceGroups,
		Start:          start,
		End:            end,
	}
	data, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/query/hourly", bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("query metricsd hourly: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("metricsd returned status %d", resp.StatusCode)
	}

	var result types.QueryHourlyResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return result.Metrics, nil
}

// queryVMsOverLimit fetches VMs that exceed included disk or bandwidth thresholds for the current month.
func (c *metricsClient) queryVMsOverLimit(ctx context.Context, vmIDs []string, diskIncludedBytes, bandwidthIncludedBytes int64) ([]types.VMOverLimit, error) {
	reqBody := types.QueryVMsOverLimitRequest{
		VMIDs:                  vmIDs,
		DiskIncludedBytes:      diskIncludedBytes,
		BandwidthIncludedBytes: bandwidthIncludedBytes,
	}
	data, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/query/vms-over-limit", bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("query metricsd vms-over-limit: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("metricsd returned status %d", resp.StatusCode)
	}

	var result types.QueryVMsOverLimitResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return result.VMs, nil
}

// queryVMsPool fetches aggregated pool history (avg/sum of CPU and memory) for the given VMs,
// along with per-VM breakdown.
func (c *metricsClient) queryVMsPool(ctx context.Context, vmNames []string, hours int) (*types.QueryVMsPoolResponse, error) {
	reqBody := types.QueryVMsPoolRequest{
		VMNames: vmNames,
		Hours:   hours,
	}
	data, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/query/vms/pool", bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("query metricsd vms pool: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("metricsd returned status %d", resp.StatusCode)
	}

	var result types.QueryVMsPoolResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &result, nil
}
