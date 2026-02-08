package testinfra

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"exe.dev/metricsd/types"
)

// MetricsdInstance describes a running metricsd process.
type MetricsdInstance struct {
	Port    int
	Address string // e.g., "http://localhost:8090"
	Cmd     *exec.Cmd
	Cancel  context.CancelFunc
	DBPath  string // temp path to DuckDB file
}

// StartMetricsd starts a metricsd instance for testing.
// It returns after the server is ready to accept connections.
func StartMetricsd(ctx context.Context, logFile io.Writer, logPorts bool) (*MetricsdInstance, error) {
	start := time.Now()
	slog.InfoContext(ctx, "starting metricsd")

	// Create a temp file path for the DuckDB database
	// We just need a unique path - DuckDB will create the file itself
	dbFile, err := os.CreateTemp("", "metricsd-test-*.duckdb")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp db file: %w", err)
	}
	dbPath := dbFile.Name()
	dbFile.Close()
	os.Remove(dbPath) // Remove the empty file so DuckDB can create it properly
	AddCleanup(func() { os.Remove(dbPath) })

	// Build metricsd binary
	rootDir, err := exeRootDir()
	if err != nil {
		return nil, err
	}

	binFile, err := os.CreateTemp("", "metricsd-test-*")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp binary file: %w", err)
	}
	binPath := binFile.Name()
	binFile.Close()
	AddCleanup(func() { os.Remove(binPath) })

	buildCmd := exec.Command("go", "build", "-o", binPath, "./cmd/metricsd")
	buildCmd.Dir = rootDir
	if out, err := buildCmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("failed to build metricsd: %w\n%s", err, out)
	}

	// Start metricsd with random port
	metricsdCtx, metricsdCancel := context.WithCancel(ctx)
	metricsdCmd := exec.CommandContext(metricsdCtx, binPath,
		"-addr", ":0",
		"-db", dbPath,
		"-stage", "test",
	)
	metricsdCmd.Env = append(os.Environ(), "LOG_FORMAT=json")

	cmdOut, err := metricsdCmd.StdoutPipe()
	if err != nil {
		metricsdCancel()
		return nil, fmt.Errorf("failed to get stdout pipe: %w", err)
	}
	metricsdCmd.Stderr = metricsdCmd.Stdout

	if err := metricsdCmd.Start(); err != nil {
		metricsdCancel()
		return nil, fmt.Errorf("failed to start metricsd: %w", err)
	}

	// Parse output to find listening address
	addrC := make(chan string, 1)
	go func() {
		scan := bufio.NewScanner(cmdOut)
		addrRE := regexp.MustCompile(`"addr":"([^"]+)"`)
		for scan.Scan() {
			line := scan.Text()
			if logFile != nil {
				fmt.Fprintf(logFile, "%s\n", line)
			}

			// Look for "starting metricsd" message with addr
			if strings.Contains(line, "starting metricsd") {
				if matches := addrRE.FindStringSubmatch(line); len(matches) > 1 {
					select {
					case addrC <- matches[1]:
					default:
					}
				}
			}
		}
		if err := scan.Err(); err != nil && !errors.Is(err, os.ErrClosed) {
			slog.WarnContext(ctx, "scanning metricsd output failed", "error", err)
		}
	}()

	// Wait for address with timeout
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()

	var addr string
	select {
	case addr = <-addrC:
	case <-timer.C:
		metricsdCmd.Process.Kill()
		metricsdCancel()
		return nil, fmt.Errorf("timeout waiting for metricsd to start")
	}

	// Parse port from addr (e.g., ":8090" or "[::]:8090")
	port := 0
	if idx := strings.LastIndex(addr, ":"); idx >= 0 {
		fmt.Sscanf(addr[idx+1:], "%d", &port)
	}
	if port == 0 {
		metricsdCmd.Process.Kill()
		metricsdCancel()
		return nil, fmt.Errorf("failed to parse port from addr: %s", addr)
	}

	instance := &MetricsdInstance{
		Port:    port,
		Address: fmt.Sprintf("http://localhost:%d", port),
		Cmd:     metricsdCmd,
		Cancel:  metricsdCancel,
		DBPath:  dbPath,
	}

	if logPorts {
		slog.InfoContext(ctx, "metricsd listening", "port", port)
	}

	slog.InfoContext(ctx, "started metricsd", "elapsed", time.Since(start).Truncate(100*time.Millisecond), "addr", instance.Address)
	return instance, nil
}

// Stop stops the metricsd instance.
func (m *MetricsdInstance) Stop(ctx context.Context) {
	if m == nil {
		return
	}
	slog.InfoContext(ctx, "stopping metricsd")

	if m.Cancel != nil {
		m.Cancel()
	}
	if m.Cmd != nil && m.Cmd.Process != nil {
		m.Cmd.Process.Kill()
		m.Cmd.Wait()
	}
}

// QueryMetrics queries metrics for a specific VM. Returns empty slice if none found.
func (m *MetricsdInstance) QueryMetrics(ctx context.Context, vmName string) ([]types.Metric, error) {
	url := fmt.Sprintf("%s/query?vm_name=%s&limit=100", m.Address, vmName)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to query metrics: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, body)
	}

	var batch types.MetricsBatch
	if err := json.NewDecoder(resp.Body).Decode(&batch); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return batch.Metrics, nil
}

// WaitForMetrics waits until at least one metric exists for the given VM name.
// Returns error if timeout expires before metrics appear.
func (m *MetricsdInstance) WaitForMetrics(ctx context.Context, vmName string, timeout time.Duration) ([]types.Metric, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error

	for time.Now().Before(deadline) {
		metrics, err := m.QueryMetrics(ctx, vmName)
		if err != nil {
			lastErr = err
			time.Sleep(500 * time.Millisecond)
			continue
		}

		if len(metrics) > 0 {
			return metrics, nil
		}

		time.Sleep(500 * time.Millisecond)
	}

	if lastErr != nil {
		return nil, fmt.Errorf("timeout waiting for metrics for VM %s (last error: %w)", vmName, lastErr)
	}
	return nil, fmt.Errorf("timeout waiting for metrics for VM %s", vmName)
}
