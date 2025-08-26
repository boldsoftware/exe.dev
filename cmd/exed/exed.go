package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"exe.dev"
	"exe.dev/ipallocator"
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
	piperAddr := flag.String("piper", ":2224", "Piper plugin gRPC server address")
	httpsAddr := flag.String("https", "", "HTTPS server address (enables TLS with Let's Encrypt)")
	dbPath := flag.String("db", "exe.db", "SQLite database path")
	devMode := flag.String("dev", "", `development mode: "" (production), "local" (local Docker), or "test" (test mode)`)
	dockerHosts := flag.String("docker-hosts", "", "Comma-separated list of DOCKER_HOST values (e.g., 'tcp://host1:2376,tcp://host2:2376')")
	containerBackend := flag.String("container-backend", "containerd", "Container backend to use: 'docker' or 'containerd' (default: containerd)")
	mdnsEnabled := flag.Bool("mdns", false, "Enable mDNS registration for dev mode (.local hostnames)")
	fakeHTTPEmail := flag.String("fake-email-server", "", "HTTP email server URL for sending emails (e.g., http://localhost:8025)")
	flag.Parse()

	// Validate dev mode
	if *devMode != "" && *devMode != "local" && *devMode != "test" {
		return fmt.Errorf(`valid dev modes are "", "local", and "test", got: %q`, *devMode)
	}

	// Setup structured logging
	exe.SetupLogger(*devMode)
	slog.Info("Starting exed server")

	// Parse container hosts and determine backend
	var hosts []string
	
	// Check for CTR_HOST first - if set, use containerd backend
	if ctrHost := os.Getenv("CTR_HOST"); ctrHost != "" {
		// CTR_HOST is set, use containerd backend with the specified host
		hosts = []string{ctrHost}
		*containerBackend = "containerd"
		slog.Info("Using containerd backend from CTR_HOST", "host", ctrHost)
	} else if *dockerHosts != "" {
		// Explicit docker hosts specified via flag
		hosts = strings.Split(*dockerHosts, ",")
		for i, h := range hosts {
			hosts[i] = strings.TrimSpace(h)
		}
	} else if *devMode != "" {
		// Default to local Docker for dev/test mode
		hosts = []string{""}
	} else {
		// Try to get from environment
		if dockerHost := os.Getenv("DOCKER_HOST"); dockerHost != "" {
			hosts = []string{dockerHost}
		}
	}

	if len(hosts) == 0 {
		slog.Warn("No container hosts specified, container functionality will be disabled", 
			"suggestion", "Use -docker-hosts flag, or set DOCKER_HOST/CTR_HOST env var")
	}

	if *dbPath == "TMP" {
		f, err := os.CreateTemp("", "exe.db")
		if err != nil {
			return fmt.Errorf("failed to create temp db file: %w", err)
		}
		*dbPath = f.Name()
		slog.Info("created temporary exe.db", "path", *dbPath)
	}

	server, err := exe.NewServerWithBackend(*httpAddr, *httpsAddr, *sshAddr, *piperAddr, *dbPath, *devMode, *fakeHTTPEmail, hosts, *containerBackend)
	if err != nil {
		return fmt.Errorf("failed to create server: %w", err)
	}

	if *mdnsEnabled {
		server.SetIPAllocator(ipallocator.NewMDNSAllocator())
	}

	if err := server.Start(); err != nil {
		return fmt.Errorf("server error: %w", err)
	}

	return nil // unreachable
}
