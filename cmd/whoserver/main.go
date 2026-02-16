package main

import (
	"fmt"
	"net/http"
	"os"
	"time"
)

func main() {
	hostname, _ := os.Hostname()

	http.HandleFunc("/debug/who", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(hostname))
	})

	http.HandleFunc("/debug/who-poll", func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		for i := 1; ; i++ {
			_, err := fmt.Fprintf(w, "%s %d %s\n", hostname, i, time.Now().Format(time.RFC3339))
			if err != nil {
				return
			}
			flusher.Flush()
			select {
			case <-time.After(10 * time.Second):
			case <-r.Context().Done():
				return
			}
		}
	})

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Return nothing
	})

	http.ListenAndServe(":80", nil)
}
