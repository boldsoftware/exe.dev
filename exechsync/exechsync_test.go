package exechsync

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
)

// Pinned ClickHouse image: ClickHouse 24.8 LTS (stable, small-ish).
// Digest pin avoids surprises if the tag is re-pushed.
const clickhouseImage = "clickhouse/clickhouse-server:24.8.4.13-alpine"

// TestClickHouseSyncViews brings up a real ClickHouse in a Docker container,
// creates the sync schema (tables + *_latest views), inserts a couple of
// extract_date snapshots per table, and asserts each view returns only the
// most recent snapshot.
//
// Gated by EXE_CLICKHOUSE_TEST=1 (or RUN_DOCKER_TESTS=1) because it needs a
// working Docker daemon and pulls a ~500MB image on first run.
func TestClickHouseSyncViews(t *testing.T) {
	if os.Getenv("EXE_CLICKHOUSE_TEST") != "1" && os.Getenv("RUN_DOCKER_TESTS") != "1" {
		t.Skip("skipping; set EXE_CLICKHOUSE_TEST=1 to run (requires docker)")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available: ", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)

	addr := startClickHouseContainer(ctx, t)

	opts := &clickhouse.Options{
		Addr:        []string{addr},
		Auth:        clickhouse.Auth{Database: "default", Username: "default"},
		DialTimeout: 5 * time.Second,
	}
	conn, err := clickhouse.Open(opts)
	if err != nil {
		t.Fatalf("clickhouse open: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	// Wait for ClickHouse to accept queries.
	var pingErr error
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		if pingErr = conn.Ping(ctx); pingErr == nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if pingErr != nil {
		t.Fatalf("clickhouse never became ready: %v", pingErr)
	}

	if err := CreateTables(ctx, conn); err != nil {
		t.Fatalf("CreateTables: %v", err)
	}

	// Verify views were created.
	for _, v := range LatestViews {
		var n uint64
		if err := conn.QueryRow(ctx,
			"SELECT count() FROM system.tables WHERE database = currentDatabase() AND name = ? AND engine = 'View'",
			v.View,
		).Scan(&n); err != nil {
			t.Fatalf("lookup view %s: %v", v.View, err)
		}
		if n != 1 {
			t.Errorf("expected view %s to exist, got count=%d", v.View, n)
		}
	}

	// Insert two snapshot dates for each table; the *_latest view must return only yesterday-vs-today's data.
	old := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := time.Date(2025, 1, 2, 0, 0, 0, 0, time.UTC)

	insertUser := func(d time.Time, id string) {
		t.Helper()
		b, err := conn.PrepareBatch(ctx, "INSERT INTO users (extract_date, user_id, email)")
		if err != nil {
			t.Fatalf("prepare users: %v", err)
		}
		if err := b.Append(d, id, id+"@example.com"); err != nil {
			t.Fatalf("append users: %v", err)
		}
		if err := b.Send(); err != nil {
			t.Fatalf("send users: %v", err)
		}
	}
	insertUser(old, "u-old-1")
	insertUser(old, "u-old-2")
	insertUser(newer, "u-new-1")
	insertUser(newer, "u-new-2")
	insertUser(newer, "u-new-3")

	var total, latest uint64
	if err := conn.QueryRow(ctx, "SELECT count() FROM users").Scan(&total); err != nil {
		t.Fatalf("count users: %v", err)
	}
	if total != 5 {
		t.Errorf("users total: got %d want 5", total)
	}
	if err := conn.QueryRow(ctx, "SELECT count() FROM users_latest").Scan(&latest); err != nil {
		t.Fatalf("count users_latest: %v", err)
	}
	if latest != 3 {
		t.Errorf("users_latest: got %d want 3 (only newest extract_date)", latest)
	}

	// Sanity: boxes_latest is empty when no rows have been inserted.
	var boxesLatest uint64
	if err := conn.QueryRow(ctx, "SELECT count() FROM boxes_latest").Scan(&boxesLatest); err != nil {
		t.Fatalf("count boxes_latest: %v", err)
	}
	if boxesLatest != 0 {
		t.Errorf("boxes_latest empty: got %d want 0", boxesLatest)
	}

	// Re-running CreateTables (which also re-creates views) must be idempotent.
	if err := CreateTables(ctx, conn); err != nil {
		t.Fatalf("CreateTables second call: %v", err)
	}
	if err := conn.QueryRow(ctx, "SELECT count() FROM users_latest").Scan(&latest); err != nil {
		t.Fatalf("count users_latest after re-create: %v", err)
	}
	if latest != 3 {
		t.Errorf("users_latest after re-create: got %d want 3", latest)
	}
}

// startClickHouseContainer launches a pinned ClickHouse image via `docker run`
// and returns a host:port address for the native TCP interface (9000).
// The container is torn down via t.Cleanup.
func startClickHouseContainer(ctx context.Context, t *testing.T) string {
	t.Helper()

	var buf [6]byte
	if _, err := rand.Read(buf[:]); err != nil {
		t.Fatalf("rand: %v", err)
	}
	name := "exe-ch-test-" + hex.EncodeToString(buf[:])

	// -P maps all exposed ports to random host ports; we then read back the
	// host mapping for 9000/tcp via `docker port`.
	runCmd := exec.CommandContext(ctx, "docker", "run", "-d", "--rm",
		"--name", name,
		"-P",
		// ulimit fix recommended by the upstream image.
		"--ulimit", "nofile=262144:262144",
		clickhouseImage,
	)
	out, err := runCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("docker run: %v\n%s", err, out)
	}

	t.Cleanup(func() {
		// Best effort teardown; ignore errors.
		stopCmd := exec.Command("docker", "rm", "-f", name)
		stopOut, stopErr := stopCmd.CombinedOutput()
		if stopErr != nil {
			t.Logf("docker rm -f %s: %v\n%s", name, stopErr, stopOut)
		}
	})

	var addr string
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		portCmd := exec.CommandContext(ctx, "docker", "port", name, "9000/tcp")
		pOut, pErr := portCmd.CombinedOutput()
		if pErr == nil {
			// Output lines look like "0.0.0.0:32777" or "[::]:32778".
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
		logCmd := exec.Command("docker", "logs", name)
		l, _ := logCmd.CombinedOutput()
		t.Fatalf("failed to resolve ClickHouse host port. logs:\n%s", l)
	}
	t.Logf("ClickHouse container %s listening on %s (image %s)", name, addr, clickhouseImage)
	return addr
}
