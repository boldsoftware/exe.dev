// Package webstatic holds a set of static files that are
// served by the web server (exed or exeprox).
package webstatic

import (
	"embed"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"time"
)

//go:embed static
var staticFS embed.FS

// Serve serves a file from the embedded static directory.
// This uses the binary's VCS build time as the modification time
// to enable HTTP caching.
func Serve(w http.ResponseWriter, r *http.Request, lg *slog.Logger, filename string) {
	subFS, err := fs.Sub(staticFS, "static")
	if err != nil {
		lg.ErrorContext(r.Context(), "static fs failed", "filename", filename, "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	f, err := subFS.Open(filename)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()

	w.Header().Set("Cache-Control", "no-cache")

	// Ensure text-based files are served with charset=utf-8 so browsers
	// (notably Safari) don't fall back to Latin-1 and mangle emoji/unicode.
	ct := mime.TypeByExtension(filepath.Ext(filename))
	if ct != "" && strings.HasPrefix(ct, "text/") || ct == "application/javascript" || ct == "application/json" {
		if !strings.Contains(ct, "charset") {
			ct += "; charset=utf-8"
		}
		w.Header().Set("Content-Type", ct)
	}

	http.ServeContent(w, r, filename, buildTime(), f.(io.ReadSeeker))
}

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
