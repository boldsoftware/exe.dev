package main

import (
	"flag"
	"log"
	"os"

	"exe.dev"
)

func main() {
	httpAddr := flag.String("http", ":8080", "HTTP server address")
	sshAddr  := flag.String("ssh", ":2222", "SSH server address")
	httpsAddr := flag.String("https", "", "HTTPS server address (enables TLS with Let's Encrypt)")
	dbPath := flag.String("db", "exe.db", "SQLite database path")
	devMode := flag.String("dev", "", "Development mode: \"\" (production), \"local\" (Docker), or \"realgke\" (real GKE with dev settings)")
	gcpProject := flag.String("gcp-project", "", "Google Cloud Project ID for container management (defaults to GOOGLE_CLOUD_PROJECT env var)")
	flag.Parse()

	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.Printf("Starting exed server...")

	// Validate dev mode
	if *devMode != "" && *devMode != "local" && *devMode != "realgke" {
		log.Printf("Invalid -dev mode: %s. Must be \"\", \"local\", or \"realgke\"", *devMode)
		os.Exit(1)
	}

	// Get GCP project from flag or environment
	projectID := *gcpProject
	if projectID == "" {
		projectID = os.Getenv("GOOGLE_CLOUD_PROJECT")
	}
	if projectID == "" && *devMode != "local" {
		log.Printf("Warning: No GCP project specified. Container functionality will be disabled.")
		log.Printf("Set GOOGLE_CLOUD_PROJECT env var or use -gcp-project flag")
	}
	
	server, err := exe.NewServer(*httpAddr, *httpsAddr, *sshAddr, *dbPath, *devMode, projectID)
	if err != nil {
		log.Printf("Failed to create server: %v", err)
		os.Exit(1)
	}
	
	if err := server.Start(); err != nil {
		log.Printf("Server error: %v", err)
		os.Exit(1)
	}
}
