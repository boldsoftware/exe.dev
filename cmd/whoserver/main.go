package main

import (
	"net/http"
	"os"
)

func main() {
	hostname, _ := os.Hostname()

	http.HandleFunc("/debug/who", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(hostname))
	})

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Return nothing
	})

	http.ListenAndServe(":80", nil)
}
