package exepipe

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"tailscale.com/net/tsaddr"
)

// exepipeHTTPServer is a simple HTTP server.
type exepipeHTTPServer struct {
	pipeInstance *PipeInstance

	ln     net.Listener
	server *http.Server
}

// setupHTTPServer sets up a small HTTP server used to serve metrics.
func setupHTTPServer(cfg *PipeConfig, pi *PipeInstance) (*exepipeHTTPServer, error) {
	if cfg.HTTPPort == "" {
		return nil, nil
	}

	addr, err := cfg.Env.TailscaleListenAddr(cfg.HTTPPort)
	if err != nil {
		return nil, fmt.Errorf("failed to determine tailscale address: %v", err)
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("failed to listen on %s: %v", addr, err)
	}

	cfg.Logger.Info("listening", "type", "HTTP", "addr", addr)

	ehs := &exepipeHTTPServer{
		pipeInstance: pi,
		ln:           ln,
	}
	ehs.server = &http.Server{
		Addr:     ln.Addr().String(),
		Handler:  ehs,
		ErrorLog: log.New(httpLogger{cfg.Logger}, "", 0),
	}

	return ehs, nil
}

// start starts serving HTTP.
func (ehs *exepipeHTTPServer) start(ctx context.Context) error {
	if ehs == nil {
		return nil
	}

	go func() {
		if err := ehs.server.Serve(ehs.ln); err != nil && err != http.ErrServerClosed {
			ehs.pipeInstance.lg.ErrorContext(ctx, "HTTP server startup failed", "error", err)
		}
	}()
	return nil
}

// stop stops serving HTTP.
func (ehs *exepipeHTTPServer) stop(ctx context.Context) {
	if ehs == nil {
		return
	}

	if err := ehs.server.Close(); err != nil {
		ehs.pipeInstance.lg.ErrorContext(ctx, "HTTP server close error", "error", err)
	}
}

// ServeHTTP implements http.Handler.
func (ehs *exepipeHTTPServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if ehs.pipeInstance.stopped.Load() {
		http.Error(w, "Server is shutting down", http.StatusServiceUnavailable)
		return
	}

	switch r.URL.Path {
	case "/health":
		ehs.handleHealth(w, r)
	case "/metrics":
		ehs.handleMetrics(w, r)
	default:
		http.NotFound(w, r)
	}
}

// handleHealth handles a /health HTTP request.
func (ehs *exepipeHTTPServer) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"status":"ok","timestamp":"%s"}`, time.Now().Format(time.RFC3339))
}

// handleMetrics handles a /metrics HTTP request.
func (ehs *exepipeHTTPServer) handleMetrics(w http.ResponseWriter, r *http.Request) {
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	remoteIP, err := netip.ParseAddr(host)
	if err != nil {
		http.Error(w, "remoteaddr check: "+r.RemoteAddr+": "+err.Error(), http.StatusInternalServerError)
		return
	}
	if !remoteIP.IsLoopback() && !tsaddr.IsTailscaleIP(remoteIP) {
		http.NotFound(w, r)
		return
	}

	handler := promhttp.HandlerFor(ehs.pipeInstance.metrics.registry, promhttp.HandlerOpts{})
	handler.ServeHTTP(w, r)
}

// httpLogger is a logger for the HTTP server.
type httpLogger struct {
	lg *slog.Logger
}

func (hl httpLogger) Write(p []byte) (int, error) {
	msg := strings.TrimSpace(string(p))
	hl.lg.Debug("net/http server error", "msg", msg)
	return len(p), nil
}
