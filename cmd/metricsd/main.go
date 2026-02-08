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
	port := flag.String("port", "21090", "HTTP listen port")
	stageName := flag.String("stage", "prod", `staging env: "prod", "staging", "local", or "test"`)
	flag.Parse()

	env, err := stage.Parse(*stageName)
	if err != nil {
		return err
	}

	logging.SetupLogger(env, nil, nil)

	ctx := context.Background()

	addr, err := env.TailscaleListenAddr(*port)
	if err != nil {
		return err
	}

	connector, db, err := metricsd.OpenDB(ctx, *dbPath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()
	defer connector.Close()

	srv := metricsd.NewServer(connector, db, env.ListenOnTailscaleOnly)
	defer srv.Close()

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}

	httpServer := &http.Server{
		Handler: srv.Handler(),
	}

	slog.InfoContext(ctx, "starting metricsd", "addr", ln.Addr().String(), "db", *dbPath, "tailscale_only", env.ListenOnTailscaleOnly)

	return httpServer.Serve(ln)
}
