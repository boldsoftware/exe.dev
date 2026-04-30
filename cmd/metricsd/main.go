// metricsd is a daemon that accepts VM metrics over HTTP and stores them in DuckDB.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"tailscale.com/client/local"
	"tailscale.com/client/tailscale"

	"exe.dev/domz"
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
	tlsPort := flag.String("tls-port", "", "HTTPS listen port (default: port+1); empty also means port+1. Set to \"0\" to disable.")
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

	// If a tailscale cert is available, also serve HTTPS on the TLS port.
	// Failures here log and continue; the plain HTTP server stays up.
	resolvedTLSPort, tlsEnabled, err := resolveTLSPort(*port, *tlsPort)
	if err != nil {
		return err
	}
	if tlsEnabled {
		if tlsCfg, tsDomain, ok := tryTailscaleTLS(ctx); ok {
			startTLSServer(ctx, env, resolvedTLSPort, tlsCfg, tsDomain, srv.Handler())
		}
	}

	return httpServer.Serve(ln)
}

// resolveTLSPort decides which port to use for HTTPS.
// flagVal == "" -> port+1; "0" -> disabled; anything else -> that port.
func resolveTLSPort(httpPort, flagVal string) (string, bool, error) {
	if flagVal == "0" {
		return "", false, nil
	}
	if flagVal != "" {
		if _, err := strconv.Atoi(flagVal); err != nil {
			return "", false, fmt.Errorf("invalid -tls-port %q: %w", flagVal, err)
		}
		return flagVal, true, nil
	}
	portNum, err := strconv.Atoi(httpPort)
	if err != nil {
		return "", false, fmt.Errorf("invalid -port %q: %w", httpPort, err)
	}
	return strconv.Itoa(portNum + 1), true, nil
}

func startTLSServer(ctx context.Context, env stage.Env, tlsPort string, tlsCfg *tls.Config, tsDomain string, handler http.Handler) {
	tlsAddr, err := env.TailscaleListenAddr(tlsPort)
	if err != nil {
		slog.ErrorContext(ctx, "metricsd TLS: listen addr", "port", tlsPort, "error", err)
		return
	}
	tlsLn, err := tls.Listen("tcp", tlsAddr, tlsCfg)
	if err != nil {
		slog.ErrorContext(ctx, "metricsd TLS: listen failed", "addr", tlsAddr, "error", err)
		return
	}
	tlsServer := &http.Server{Handler: handler}
	go func() {
		slog.InfoContext(ctx, "starting metricsd TLS", "addr", tlsLn.Addr().String(), "domain", tsDomain)
		if err := tlsServer.Serve(tlsLn); err != nil && err != http.ErrServerClosed {
			slog.ErrorContext(ctx, "metricsd TLS server exited", "error", err)
		}
	}()
}

var tailscaleAcknowledgeUnstableAPI = sync.OnceFunc(func() {
	tailscale.I_Acknowledge_This_API_Is_Unstable = true
})

// tryTailscaleTLS attempts to obtain a tailscale cert for the local node and
// returns a *tls.Config that uses GetCertificate to refresh as needed. If
// tailscale is unavailable or no initial cert can be fetched, it returns
// ok=false and logs an error.
func tryTailscaleTLS(ctx context.Context) (*tls.Config, string, bool) {
	tailscaleAcknowledgeUnstableAPI()

	initCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	var lc local.Client
	st, err := lc.Status(initCtx)
	if err != nil || st == nil || st.Self == nil || st.Self.DNSName == "" {
		if err != nil {
			slog.ErrorContext(ctx, "tailscale status unavailable", "error", err)
		} else {
			slog.ErrorContext(ctx, "tailscale DNS name not found")
		}
		return nil, "", false
	}
	tsDomain := domz.Canonicalize(st.Self.DNSName)

	initial, err := loadTailscaleCert(initCtx, tsDomain)
	if err != nil {
		slog.ErrorContext(ctx, "tailscale cert pair not preloaded", "domain", tsDomain, "error", err)
		return nil, "", false
	}
	slog.InfoContext(ctx, "tailscale cert loaded", "domain", tsDomain)

	var (
		mu         sync.Mutex
		cached     = initial
		refreshing bool
	)
	getCert := func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
		mu.Lock()
		cur := cached
		fresh := cur != nil && cur.Leaf != nil && time.Now().Before(cur.Leaf.NotAfter.Add(-24*time.Hour))
		startRefresh := !fresh && !refreshing
		if startRefresh {
			refreshing = true
		}
		mu.Unlock()

		if fresh || !startRefresh {
			// Either current cert is good, or another handshake is
			// already refreshing; serve what we have.
			if cur != nil {
				return cur, nil
			}
		}

		fetchCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		c, err := loadTailscaleCert(fetchCtx, tsDomain)

		mu.Lock()
		refreshing = false
		if err == nil {
			cached = c
		}
		cur = cached
		mu.Unlock()

		if err != nil {
			if cur != nil {
				slog.ErrorContext(fetchCtx, "tailscale cert refresh failed; using cached", "error", err)
				return cur, nil
			}
			return nil, err
		}
		slog.InfoContext(fetchCtx, "tailscale cert refreshed", "domain", tsDomain)
		return cur, nil
	}

	return &tls.Config{
		GetCertificate: getCert,
		NextProtos:     []string{"http/1.1"},
	}, tsDomain, true
}

func loadTailscaleCert(ctx context.Context, domain string) (*tls.Certificate, error) {
	var lc local.Client
	certPEM, keyPEM, err := lc.CertPair(ctx, domain)
	if err != nil {
		return nil, fmt.Errorf("cert pair: %w", err)
	}
	c, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("x509 keypair: %w", err)
	}
	if len(c.Certificate) > 0 {
		if leaf, err := x509.ParseCertificate(c.Certificate[0]); err == nil {
			c.Leaf = leaf
		}
	}
	return &c, nil
}
