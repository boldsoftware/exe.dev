// Package webstatic holds a set of static files that are
// served by the web server (exed or exeprox).
package webstatic

import (
	"embed"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"runtime/debug"
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
