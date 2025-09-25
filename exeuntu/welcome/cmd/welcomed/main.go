package main

import (
	"log"
	"os"

	"exe.dev/exeuntu/welcome/srv"
)

func main() {
	dbPath := os.Getenv("WELCOME_DB_PATH")
	hostname, err := os.Hostname()
	if err != nil {
		hostname = "unknown"
	}

	server := srv.New(nil, hostname)

	if err := server.SetupDatabase(dbPath); err != nil {
		log.Fatalf("database setup failed: %v", err)
	}
	defer server.DB.Close()

	log.Fatal(server.Serve(":8000"))
}
