// metricsd is a daemon that accepts VM metrics over HTTP and stores them in DuckDB.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"exe.dev/logging"
	"exe.dev/metricsd"
	"exe.dev/stage"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	dbPath := flag.String("db", "metrics.duckdb", "path to DuckDB database file")
	archiveDir := flag.String("archive-dir", "", "directory for parquet archive files (default: <db-dir>/archive)")
	port := flag.String("port", "21090", "HTTP listen port")
	stageName := flag.String("stage", "prod", `staging env: "prod", "staging", "local", or "test"`)
	flag.Parse()

	env, err := stage.Parse(*stageName)
	if err != nil {
		return err
	}

	logging.SetupLogger(env, nil, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	addr, err := env.TailscaleListenAddr(*port)
	if err != nil {
		return err
	}

	// Default archive dir to <db-dir>/archive.
	aDir := *archiveDir
	if aDir == "" && *dbPath != "" {
		aDir = filepath.Join(filepath.Dir(*dbPath), "archive")
	}

	connector, db, archiver, err := metricsd.OpenDB(ctx, *dbPath, aDir)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()
	defer connector.Close()

	// Start periodic archival (every hour).
	if archiver != nil {
		archiver.RunPeriodic(ctx, 1*time.Hour)
	}

	// Start periodic rollup (every hour).
	metricsd.NewRollup(db).RunPeriodic(ctx, time.Hour)

	srv := metricsd.NewServer(connector, db, env.ListenOnTailscaleOnly)
	defer srv.Close()

	// Optional ClickHouse mirror: every batch written to DuckDB is also
	// streamed into a ClickHouse vm_metrics table. Controlled by
	// CLICKHOUSE_METRICS_DSN; no-op when unset.
	if dsn := os.Getenv("CLICKHOUSE_METRICS_DSN"); dsn != "" {
		chSync, err := metricsd.StartClickHouseSync(ctx, metricsd.ClickHouseConfig{
			DSN:    dsn,
			Logger: slog.Default(),
		})
		if err != nil {
			slog.ErrorContext(ctx, "failed to start clickhouse mirror", "error", err)
		} else {
			srv.SetClickHouse(chSync)
			defer chSync.Close()
			slog.InfoContext(ctx, "metricsd: clickhouse mirror enabled")
		}
	}

	go func() {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
		for sig := range ch {
			slog.InfoContext(ctx, "received signal, shutting down", "signal", sig)
			cancel()
			archiver.WaitUntilStopped()
			srv.Close()
			os.Exit(0)
		}
	}()

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}

	httpServer := &http.Server{
		Handler: srv.Handler(),
	}

	slackFeed := logging.NewSlackFeed(slog.Default(), env)
	slackFeed.ServiceStarted(ctx, "metricsd")

	slog.InfoContext(ctx, "starting metricsd", "addr", ln.Addr().String(), "db", *dbPath, "archive_dir", aDir, "tailscale_only", env.ListenOnTailscaleOnly)

	return httpServer.Serve(ln)
}
