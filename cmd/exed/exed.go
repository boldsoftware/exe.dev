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
	devMode := flag.String("dev", "", "Development mode: \"\" (production) or \"local\" (local Docker)")
	dockerHosts := flag.String("docker-hosts", "", "Comma-separated list of DOCKER_HOST values (e.g., 'tcp://host1:2376,tcp://host2:2376')")
	mdnsEnabled := flag.Bool("mdns", false, "Enable mDNS registration for dev mode (.local hostnames)")
	flag.Parse()

	// Validate dev mode
	if *devMode != "" && *devMode != "local" {
		return fmt.Errorf(`valid dev modes are "" and "local", got: %q`, *devMode)
	}

	// Setup structured logging
	exe.SetupLogger(*devMode)
	slog.Info("Starting exed server")

	// Parse Docker hosts
	var hosts []string
	if *dockerHosts != "" {
		hosts = strings.Split(*dockerHosts, ",")
		for i, h := range hosts {
			hosts[i] = strings.TrimSpace(h)
		}
	} else if *devMode == "local" {
		// Default to local Docker for dev mode
		hosts = []string{""}
	} else {
		// Try to get from environment
		if dockerHost := os.Getenv("DOCKER_HOST"); dockerHost != "" {
			hosts = []string{dockerHost}
		}
	}

	if len(hosts) == 0 {
		slog.Warn("No Docker hosts specified, container functionality will be disabled", "suggestion", "Use -docker-hosts flag or set DOCKER_HOST env var")
	}

	server, err := exe.NewServer(*httpAddr, *httpsAddr, *sshAddr, *piperAddr, *dbPath, *devMode, hosts)
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
