package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"exe.dev/cmd/pktflowaggd/pktflowagg"
)

func main() {
	var listenAddr string
	var maxIntervals int
	var exelets string
	flag.StringVar(&listenAddr, "listen", ":8088", "listen address")
	flag.IntVar(&maxIntervals, "max-intervals", 60, "number of intervals to keep in memory per VM")
	flag.StringVar(&exelets, "exelets", "", "comma-separated gRPC addresses of exelets")
	flag.Parse()

	registry := prometheus.NewRegistry()
	st := pktflowagg.NewStore(maxIntervals, registry)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if exelets != "" {
		for _, addr := range strings.Split(exelets, ",") {
			addr = strings.TrimSpace(addr)
			if addr == "" {
				continue
			}
			st.RegisterExelet(addr)
			go pktflowagg.ConsumeExelet(ctx, addr, st)
		}
	}

	server := &http.Server{
		Addr:              listenAddr,
		Handler:           pktflowagg.NewMux(registry, st),
		ReadHeaderTimeout: 5 * time.Second,
	}
	log.Printf("pktflowaggd listening on %s", listenAddr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("pktflowaggd failed: %v", err)
	}
}
