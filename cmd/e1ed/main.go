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
	"syscall"
	"time"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	poolSize := flag.Int("pool-size", 2, "number of hot spare environments")
	repoPath := flag.String("repo-path", "/data/e1ed/repo.git", "bare repo location")
	listenAddr := flag.String("listen", "127.0.0.1:7723", "listen address")
	maxIdle := flag.Duration("max-idle", 4*time.Hour, "recycle VMs idle longer than this")
	flag.Parse()

	if _, err := os.Stat(*repoPath); err != nil {
		return fmt.Errorf("repo path %s: %w", *repoPath, err)
	}

	slog.Info("starting e1ed",
		"pool_size", *poolSize,
		"repo_path", *repoPath,
		"listen", *listenAddr,
		"max_idle", *maxIdle,
	)

	// Resolve ops/ directory from the bare repo.
	opsDir, err := resolveOpsDir(*repoPath)
	if err != nil {
		return fmt.Errorf("resolve ops dir: %w", err)
	}
	slog.Info("ops directory", "path", opsDir)

	pool := NewPool(*poolSize, *maxIdle, opsDir, &ShellExecutor{})
	pool.Start()

	srv := NewServer(pool, *repoPath)

	ln, err := net.Listen("tcp", *listenAddr)
	if err != nil {
		pool.Stop()
		return fmt.Errorf("listen: %w", err)
	}

	httpServer := &http.Server{
		Handler:     srv.Handler(),
		ReadTimeout: 30 * time.Second,
		// No write timeout: /run streams for the duration of a test.
	}

	// Graceful shutdown on SIGINT/SIGTERM.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		<-ctx.Done()
		slog.InfoContext(ctx, "shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		httpServer.Shutdown(shutdownCtx)
	}()

	slog.InfoContext(ctx, "listening", "addr", ln.Addr().String())
	if err := httpServer.Serve(ln); err != nil && err != http.ErrServerClosed {
		pool.Stop()
		return fmt.Errorf("serve: %w", err)
	}

	pool.Stop()
	slog.InfoContext(ctx, "stopped")
	return nil
}
