package main

import (
	"bytes"
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	blogpkg "exe.dev/blog"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"tailscale.com/net/tsaddr"
)

// Embed minimal static assets needed by blog templates.
//
//go:embed static/*
var staticFS embed.FS

// buildTime returns the VCS commit time from build info, or the process start time as fallback.
// Used as the modification time for embedded static files to enable HTTP caching.
var buildTime = sync.OnceValue(func() time.Time {
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			if setting.Key == "vcs.time" {
				if t, err := time.Parse(time.RFC3339, setting.Value); err == nil {
					return t
				}
			}
		}
	}
	return time.Now()
})

func main() {
	httpAddr := flag.String("http", ":8080", "HTTP server address, empty to disable")
	flag.Parse()

	if *httpAddr == "" {
		// Explicitly fail if address is empty to avoid surprising behavior.
		panic("-http must be set to a non-empty address")
	}

	metricsRegistry := prometheus.NewRegistry()
	metricsRegistry.MustRegister(prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{}))
	metricsRegistry.MustRegister(prometheus.NewGoCollector())

	blogMetrics := blogpkg.NewMetrics(metricsRegistry)
	log := slog.Default()

	var handleBlog func(http.ResponseWriter, *http.Request) bool
	if liveDir := detectLiveBlogDir(); liveDir != "" {
		var err error
		handleBlog, err = newLiveReloadHandler(log, liveDir, blogMetrics)
		if err != nil {
			log.Error("failed to initialize live reload", "dir", liveDir, "error", err)
			panic(err)
		}
		log.Info("live reload enabled", "dir", liveDir)
	} else {
		store, err := blogpkg.Load(true /* includeUnpublished */)
		if err != nil {
			log.Error("failed to load blog store", "error", err)
			panic(err)
		}
		handler := blogpkg.NewHandler(store, false /* showHidden */)
		if handler == nil {
			panic("blog handler is nil")
		}
		handleBlog = handler.WithMetrics(blogMetrics).Handle
	}

	mux := http.NewServeMux()

	// Health endpoint
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"status":"ok","timestamp":"%s"}`+"\n", time.Now().Format(time.RFC3339))
	})

	// Serve favicon and apple touch icon from embedded static assets.
	mux.HandleFunc("/favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		serveStaticFile(w, r, "favicon.ico")
	})
	mux.HandleFunc("/apple-touch-icon.png", func(w http.ResponseWriter, r *http.Request) {
		serveStaticFile(w, r, "apple-touch-icon.png")
	})

	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) {
		serveStaticFile(w, r, "robots.txt")
	})

	// Serve embedded static assets under /static/
	mux.HandleFunc("/static/", func(w http.ResponseWriter, r *http.Request) {
		filename := strings.TrimPrefix(r.URL.Path, "/static/")
		if filename == "" || strings.Contains(filename, "..") {
			http.NotFound(w, r)
			return
		}
		serveStaticFile(w, r, filename)
	})

	mux.Handle("/metrics", requireTailnetAccess(promhttp.HandlerFor(metricsRegistry, promhttp.HandlerOpts{})))

	// Blog routes
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if handleBlog(w, r) {
			return
		}

		http.NotFound(w, r)
	})

	srv := &http.Server{
		Addr:              *httpAddr,
		Handler:           logRequests(mux),
		ReadHeaderTimeout: 5 * time.Second,
	}

	log.Info("starting blogd", "url", "http://localhost"+*httpAddr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Error("server error", "error", err)
		panic(err)
	}
}

func detectLiveBlogDir() string {
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	root, ok := findGitRoot(wd)
	if !ok {
		return ""
	}
	blogDir := filepath.Join(root, "blog")
	if _, err := os.Stat(blogDir); err != nil {
		return ""
	}
	return blogDir
}

func findGitRoot(start string) (string, bool) {
	if start == "" {
		return "", false
	}
	dir := start
	if !filepath.IsAbs(dir) {
		if abs, err := filepath.Abs(dir); err == nil {
			dir = abs
		}
	}
	dir = filepath.Clean(dir)
	for {
		path := filepath.Join(dir, ".git")
		if info, err := os.Stat(path); err == nil && (info.IsDir() || info.Mode().IsRegular()) {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", false
}

func newLiveReloadHandler(log *slog.Logger, blogDir string, metrics *blogpkg.Metrics) (func(http.ResponseWriter, *http.Request) bool, error) {
	if _, err := blogpkg.LoadFromDir(blogDir, true); err != nil {
		return nil, err
	}
	if _, _, err := blogpkg.ParseTemplatesFromDir(blogDir); err != nil {
		return nil, err
	}
	return func(w http.ResponseWriter, r *http.Request) bool {
		store, err := blogpkg.LoadFromDir(blogDir, true)
		if err != nil {
			log.Error("failed to reload blog store", "error", err)
			http.Error(w, "error reloading blog content", http.StatusInternalServerError)
			return true
		}
		blogTmpl, atomTmpl, err := blogpkg.ParseTemplatesFromDir(blogDir)
		if err != nil {
			log.Error("failed to reload blog templates", "error", err)
			http.Error(w, "error reloading blog templates", http.StatusInternalServerError)
			return true
		}
		handler := blogpkg.NewHandlerWithTemplates(store, true, blogTmpl, atomTmpl)
		if handler == nil {
			http.Error(w, "blog unavailable", http.StatusInternalServerError)
			return true
		}
		return handler.WithMetrics(metrics).Handle(w, r)
	}, nil
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (w *statusRecorder) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusRecorder) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(recorder, r)

		status := recorder.status
		if status == 0 {
			status = http.StatusOK
		}

		var xHeaders map[string][]string
		for k, v := range r.Header {
			if strings.HasPrefix(k, "X-") {
				if xHeaders == nil {
					xHeaders = make(map[string][]string)
				}
				xHeaders[k] = v
			}
		}

		entry := struct {
			Path       string              `json:"path"`
			Method     string              `json:"method"`
			Status     int                 `json:"status"`
			RemoteAddr string              `json:"remote_addr"`
			XHeaders   map[string][]string `json:"x_headers,omitempty"`
		}{
			Path:       r.URL.Path,
			Method:     r.Method,
			Status:     status,
			RemoteAddr: r.RemoteAddr,
			XHeaders:   xHeaders,
		}

		if err := json.NewEncoder(os.Stdout).Encode(entry); err != nil {
			slog.Error("failed to log request", "error", err)
		}
	})
}

func requireTailnetAccess(handler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, port, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			http.Error(w, "remoteaddr check: "+r.RemoteAddr+": "+err.Error(), http.StatusInternalServerError)
			return
		}
		if port == "" {
			http.Error(w, "remoteaddr check: missing port in "+r.RemoteAddr, http.StatusInternalServerError)
			return
		}
		remoteIP, err := netip.ParseAddr(host)
		if err != nil {
			http.Error(w, "remoteaddr check: "+r.RemoteAddr+": "+err.Error(), http.StatusInternalServerError)
			return
		}
		if !remoteIP.IsLoopback() && !tsaddr.IsTailscaleIP(remoteIP) {
			http.Error(w, "Access denied", http.StatusUnauthorized)
			return
		}
		handler.ServeHTTP(w, r)
	})
}

// serveStaticFile serves a file from the embedded static directory.
func serveStaticFile(w http.ResponseWriter, r *http.Request, filename string) {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	f, err := sub.Open(filename)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	http.ServeContent(w, r, filename, buildTime(), bytes.NewReader(data))
}
