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
	flag.Parse()

	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.Printf("Starting exed server...")

	server := exe.NewServer(*httpAddr, *httpsAddr, *sshAddr)
	
	if err := server.Start(); err != nil {
		log.Printf("Server error: %v", err)
		os.Exit(1)
	}
}
