package main

import (
	"flag"
	"log/slog"
	"os"
	"strings"

	"exe.dev"
)

func main() {
	httpAddr := flag.String("http", ":8080", "HTTP server address")
	sshAddr := flag.String("ssh", ":2223", "SSH server address")
	piperAddr := flag.String("piper", ":2224", "Piper plugin gRPC server address")
	httpsAddr := flag.String("https", "", "HTTPS server address (enables TLS with Let's Encrypt)")
	dbPath := flag.String("db", "exe.db", "SQLite database path")
	devMode := flag.String("dev", "", "Development mode: \"\" (production) or \"local\" (local Docker)")
	dockerHosts := flag.String("docker-hosts", "", "Comma-separated list of DOCKER_HOST values (e.g., 'tcp://host1:2376,tcp://host2:2376')")
	flag.Parse()

	// Validate dev mode
	if *devMode != "" && *devMode != "local" {
		// Setup basic logging first for error reporting
		exe.SetupLogger(*devMode)
		slog.Error("Invalid dev mode", "mode", *devMode, "valid_options", []string{"", "local"})
		os.Exit(1)
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
		slog.Error("Failed to create server", "error", err)
		os.Exit(1)
	}

	if err := server.Start(); err != nil {
		slog.Error("Server error", "error", err)
		os.Exit(1)
	}
}
