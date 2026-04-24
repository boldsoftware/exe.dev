package metricsd

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"exe.dev/metricsd/types"

	"github.com/ClickHouse/clickhouse-go/v2"
)

// Pinned ClickHouse image (mirrors exechsync).
const testClickHouseImage = "clickhouse/clickhouse-server:24.8.4.13-alpine"

// TestClickHouseMirror brings up a real ClickHouse container and asserts
// that metrics posted to metricsd /write are asynchronously mirrored into
// the ClickHouse vm_metrics table with matching columns.
//
// Gated by EXE_CLICKHOUSE_TEST=1 (or RUN_DOCKER_TESTS=1).
func TestClickHouseMirror(t *testing.T) {
	if os.Getenv("EXE_CLICKHOUSE_TEST") != "1" && os.Getenv("RUN_DOCKER_TESTS") != "1" {
		t.Skip("skipping; set EXE_CLICKHOUSE_TEST=1 to run (requires docker)")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available: ", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)

	addr := startMetricsdClickHouseContainer(ctx, t)
	dsn := fmt.Sprintf("clickhouse://default:@%s/default", addr)

	// Wait for ClickHouse to be fully ready via direct ping.
	waitConn, err := clickhouse.Open(&clickhouse.Options{
		Addr:        []string{addr},
		Auth:        clickhouse.Auth{Database: "default", Username: "default"},
		DialTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("ch open for wait: %v", err)
	}
	deadline := time.Now().Add(90 * time.Second)
	var pingErr error
	for time.Now().Before(deadline) {
		if pingErr = waitConn.Ping(ctx); pingErr == nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	waitConn.Close()
	if pingErr != nil {
		t.Fatalf("clickhouse never ready: %v", pingErr)
	}

	// Bring up metricsd.
	connector, db, _, err := OpenDB(ctx, "", "")
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { db.Close(); connector.Close() })

	srv := NewServer(connector, db, false)
	t.Cleanup(func() { srv.Close() })

	ch, err := StartClickHouseSync(ctx, ClickHouseConfig{DSN: dsn})
	if err != nil {
		t.Fatalf("StartClickHouseSync: %v", err)
	}
	if ch == nil {
		t.Fatal("StartClickHouseSync returned nil with a DSN")
	}
	t.Cleanup(func() { ch.Close() })
	srv.SetClickHouse(ch)

	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	// Post a batch.
	now := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)
	batch := types.MetricsBatch{
		Metrics: []types.Metric{
			{
				Timestamp: now, Host: "exelet-01", VMName: "vm-a", ResourceGroup: "acct-a", VMID: "vid-a",
				DiskSizeBytes: 10, DiskUsedBytes: 5, DiskLogicalUsedBytes: 7,
				MemoryNominalBytes: 100, MemoryRSSBytes: 50, MemorySwapBytes: 1,
				CPUUsedCumulativeSecs: 1.5, CPUNominal: 2,
				NetworkTXBytes: 1000, NetworkRXBytes: 2000,
				IOReadBytes: 300, IOWriteBytes: 400,
			},
			{
				Timestamp: now.Add(time.Second), Host: "exelet-01", VMName: "vm-b",
				DiskSizeBytes:         20,
				CPUUsedCumulativeSecs: 9.5,
			},
		},
	}
	postJSON(t, ts.URL+"/write", batch)

	// Open a separate connection to verify.
	verifyConn, err := clickhouse.Open(&clickhouse.Options{
		Addr:        []string{addr},
		Auth:        clickhouse.Auth{Database: "default", Username: "default"},
		DialTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("ch open verify: %v", err)
	}
	t.Cleanup(func() { verifyConn.Close() })

	// The mirror is async; poll for rows to arrive.
	var count uint64
	deadline = time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if err := verifyConn.QueryRow(ctx, "SELECT count() FROM vm_metrics").Scan(&count); err == nil && count >= 2 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if count != 2 {
		t.Fatalf("expected 2 rows in ClickHouse vm_metrics, got %d", count)
	}

	// Spot check vm-a.
	var (
		vmName, host, rg, vmID string
		diskUsed               int64
		cpuCum                 float64
		tsOut                  time.Time
	)
	row := verifyConn.QueryRow(ctx,
		"SELECT timestamp, host, vm_name, resource_group, vm_id, disk_used_bytes, cpu_used_cumulative_seconds FROM vm_metrics WHERE vm_name = 'vm-a'")
	if err := row.Scan(&tsOut, &host, &vmName, &rg, &vmID, &diskUsed, &cpuCum); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if vmName != "vm-a" || host != "exelet-01" || rg != "acct-a" || vmID != "vid-a" {
		t.Errorf("unexpected row: vm_name=%q host=%q rg=%q vm_id=%q", vmName, host, rg, vmID)
	}
	if diskUsed != 5 || cpuCum != 1.5 {
		t.Errorf("unexpected metrics: disk_used=%d cpu_cum=%v", diskUsed, cpuCum)
	}
	if !tsOut.Equal(now) {
		t.Errorf("timestamp: got %v want %v", tsOut, now)
	}

	// Enqueue a second batch via direct API to ensure Close cleans up.
	ch.Enqueue([]types.Metric{{VMName: "vm-c", Timestamp: now.Add(2 * time.Second)}})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		_ = verifyConn.QueryRow(ctx, "SELECT count() FROM vm_metrics").Scan(&count)
		if count >= 3 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if count != 3 {
		t.Errorf("expected 3 rows after direct Enqueue, got %d", count)
	}
}

// TestClickHouseSyncNoDSN asserts StartClickHouseSync is a no-op when the
// DSN is empty, and that SetClickHouse(nil) is equivalent to not
// configuring the mirror.
func TestClickHouseSyncNoDSN(t *testing.T) {
	ctx := context.Background()
	ch, err := StartClickHouseSync(ctx, ClickHouseConfig{DSN: ""})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ch != nil {
		t.Fatalf("expected nil sync, got %v", ch)
	}
	// Enqueue and Close are safe on nil.
	ch.Enqueue([]types.Metric{{VMName: "x"}})
	if err := ch.Close(); err != nil {
		t.Fatalf("Close on nil: %v", err)
	}
}

func postJSON(t *testing.T, url string, body any) {
	t.Helper()
	buf := mustJSON(t, body)
	resp, err := http.Post(url, "application/json", strings.NewReader(buf))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		t.Fatalf("POST %s status=%d", url, resp.StatusCode)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

func startMetricsdClickHouseContainer(ctx context.Context, t *testing.T) string {
	t.Helper()

	var buf [6]byte
	if _, err := rand.Read(buf[:]); err != nil {
		t.Fatalf("rand: %v", err)
	}
	name := "metricsd-ch-test-" + hex.EncodeToString(buf[:])

	runCmd := exec.CommandContext(ctx, "docker", "run", "-d", "--rm",
		"--name", name,
		"-P",
		"--ulimit", "nofile=262144:262144",
		testClickHouseImage,
	)
	out, err := runCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("docker run: %v\n%s", err, out)
	}

	t.Cleanup(func() {
		stopCmd := exec.Command("docker", "rm", "-f", name)
		if o, err := stopCmd.CombinedOutput(); err != nil {
			t.Logf("docker rm -f %s: %v\n%s", name, err, o)
		}
	})

	var addr string
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		portCmd := exec.CommandContext(ctx, "docker", "port", name, "9000/tcp")
		pOut, pErr := portCmd.CombinedOutput()
		if pErr == nil {
			for _, line := range strings.Split(strings.TrimSpace(string(pOut)), "\n") {
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}
				if i := strings.LastIndex(line, ":"); i > 0 {
					addr = "127.0.0.1:" + line[i+1:]
					break
				}
			}
		}
		if addr != "" {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if addr == "" {
		l, _ := exec.Command("docker", "logs", name).CombinedOutput()
		t.Fatalf("no port map. logs:\n%s", l)
	}
	t.Logf("ClickHouse %s on %s", name, addr)
	return addr
}
