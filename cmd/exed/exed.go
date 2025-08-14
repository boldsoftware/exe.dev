package main

import (
	"flag"
	"log"
	"os"
	"strings"

	"exe.dev"
)

func main() {
	httpAddr := flag.String("http", ":8080", "HTTP server address")
	sshAddr := flag.String("ssh", ":2222", "SSH server address")
	httpsAddr := flag.String("https", "", "HTTPS server address (enables TLS with Let's Encrypt)")
	dbPath := flag.String("db", "exe.db", "SQLite database path")
	devMode := flag.String("dev", "", "Development mode: \"\" (production) or \"local\" (local Docker)")
	dockerHosts := flag.String("docker-hosts", "", "Comma-separated list of DOCKER_HOST values (e.g., 'tcp://host1:2376,tcp://host2:2376')")
	flag.Parse()

	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.Printf("Starting exed server...")

	// Validate dev mode
	if *devMode != "" && *devMode != "local" {
		log.Printf("Invalid -dev mode: %s. Must be \"\" or \"local\"", *devMode)
		os.Exit(1)
	}

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
		log.Printf("Warning: No Docker hosts specified. Container functionality will be disabled.")
		log.Printf("Use -docker-hosts flag or set DOCKER_HOST env var")
	}

	server, err := exe.NewServer(*httpAddr, *httpsAddr, *sshAddr, *dbPath, *devMode, hosts)
	if err != nil {
		log.Printf("Failed to create server: %v", err)
		os.Exit(1)
	}

	if err := server.Start(); err != nil {
		log.Printf("Server error: %v", err)
		os.Exit(1)
	}
}
