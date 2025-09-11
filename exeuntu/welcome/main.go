package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"time"
)

func main() {
	hostname, err := os.Hostname()
	if err != nil {
		hostname = "unknown"
	}

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("Request from %s: %s %s", r.RemoteAddr, r.Method, r.URL.Path)
		fmt.Fprintf(w, "Welcome to exe.dev!\n\nHostname: %s\nTime: %s\nPath: %s\n",
			hostname, time.Now().Format(time.RFC3339), r.URL.Path)
	})

	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "OK\n")
	})

	log.Println("Starting web server on :8000")
	log.Fatal(http.ListenAndServe(":8000", nil))
}
