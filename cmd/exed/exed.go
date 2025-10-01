package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"exe.dev"
	"exe.dev/ctrhosttest"
	"exe.dev/logging"
)

func main() {
	err := run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	httpAddr := flag.String("http", ":8080", "HTTP server address")
	sshAddr := flag.String("ssh", ":2223", "SSH server address")
	pluginAddr := flag.String("piper-plugin", ":2224", "Piper plugin gRPC server address")
	piperdPort := flag.Int("piperd-port", 2222, "sshpiper listening port")
	httpsAddr := flag.String("https", "", "HTTPS server address (enables TLS with Let's Encrypt)")
	dbPath := flag.String("db", "exe.db", "SQLite database path")
	devMode := flag.String("dev", "", `development mode: "" (production), "local" (local containerd), or "test" (test mode)`)
	containerdAddresses := flag.String("containerd-addresses", "", "Comma-separated list of containerd addresses (e.g., 'ssh://host1,ssh://host2')")
	fakeHTTPEmail := flag.String("fake-email-server", "", "HTTP email server URL for sending emails (e.g., http://localhost:8025)")
	flag.Parse()

	// Validate dev mode
	if *devMode != "" && *devMode != "local" && *devMode != "test" {
		return fmt.Errorf(`valid dev modes are "", "local", and "test", got: %q`, *devMode)
	}

	// Setup structured logging
	logging.SetupLogger(*devMode)
	slog.Info("Starting exed server")

	// Parse containerd addresses
	var addresses []string

	if *containerdAddresses != "" {
		// Explicit containerd addresses specified via flag
		addresses = strings.Split(*containerdAddresses, ",")
		for i, h := range addresses {
			addresses[i] = strings.TrimSpace(h)
		}
	} else if *devMode == "local" || *devMode == "test" {
		ctrHost := ctrhosttest.Detect()
		if ctrHost == "" {
			return fmt.Errorf("dev mode: could not detect ctr-host")
		}
		addresses = []string{ctrHost}
	}

	if len(addresses) == 0 {
		slog.Warn("No containerd addresses specified, container functionality will be disabled",
			"suggestion", "Use -containerd-addresses flag, or set CTR_HOST env var")
	}

	if *dbPath == "TMP" {
		f, err := os.CreateTemp("", "exe.db")
		if err != nil {
			return fmt.Errorf("failed to create temp db file: %w", err)
		}
		*dbPath = f.Name()
		slog.Info("created temporary exe.db", "path", *dbPath)
	}

	server, err := exe.NewServer(*httpAddr, *httpsAddr, *sshAddr, *pluginAddr, *dbPath, *devMode, *fakeHTTPEmail, *piperdPort, addresses)
	if err != nil {
		return fmt.Errorf("failed to create server: %w", err)
	}

	if err := server.Start(); err != nil {
		return fmt.Errorf("server error: %w", err)
	}

	return nil // unreachable
}
