package main

import (
	"embed"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	blogpkg "exe.dev/blog"
)

// Embed minimal static assets needed by blog templates.
//
//go:embed static/*
var staticFS embed.FS

func main() {
	httpAddr := flag.String("http", ":8080", "HTTP server address, empty to disable")
	flag.Parse()

	if *httpAddr == "" {
		// Explicitly fail if address is empty to avoid surprising behavior.
		panic("-http must be set to a non-empty address")
	}

	log := slog.Default()

	var handleBlog func(http.ResponseWriter, *http.Request) bool
	if liveDir := detectLiveBlogDir(); liveDir != "" {
		var err error
		handleBlog, err = newLiveReloadHandler(log, liveDir)
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
		handleBlog = handler.Handle
	}

	mux := http.NewServeMux()

	// Health endpoint
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"status":"ok","timestamp":"%s"}`+"\n", time.Now().Format(time.RFC3339))
	})

	// Serve embedded static assets under /static/
	mux.HandleFunc("/static/", func(w http.ResponseWriter, r *http.Request) {
		sub, err := fs.Sub(staticFS, "static")
		if err != nil {
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}
		filename := strings.TrimPrefix(r.URL.Path, "/static/")
		if filename == "" || strings.Contains(filename, "..") {
			http.NotFound(w, r)
			return
		}
		tmpReq := r.Clone(r.Context())
		tmpReq.URL.Path = "/" + filename
		http.FileServer(http.FS(sub)).ServeHTTP(w, tmpReq)
	})

	// Blog routes
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if handleBlog(w, r) {
			return
		}

		http.NotFound(w, r)
	})

	srv := &http.Server{
		Addr:              *httpAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	log.Info("starting blogd", "addr", *httpAddr)
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

func newLiveReloadHandler(log *slog.Logger, blogDir string) (func(http.ResponseWriter, *http.Request) bool, error) {
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
		return handler.Handle(w, r)
	}, nil
}
