package exeweb

import (
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
)

// ServePrecompressed serves a file from fsys, preferring a pre-compressed
// .gz sibling when the client accepts gzip encoding. If no .gz file exists
// or the client doesn't accept gzip, the original file is served normally.
//
// The caller is responsible for setting Cache-Control headers before calling.
func ServePrecompressed(w http.ResponseWriter, r *http.Request, fsys fs.FS, name string) {
	// Only attempt .gz for file types we pre-compress.
	if acceptsGzip(r) && isPrecompressedExt(name) {
		gzName := name + ".gz"
		if f, err := fsys.Open(gzName); err == nil {
			defer f.Close()
			stat, err := f.Stat()
			if err == nil && !stat.IsDir() {
				serveGzFile(w, name, f)
				return
			}
		}
	}
	// Fallback: serve uncompressed.
	http.ServeFileFS(w, r, fsys, name)
}

func serveGzFile(w http.ResponseWriter, origName string, f fs.File) {
	ct := mime.TypeByExtension(path.Ext(origName))
	if ct == "" {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Encoding", "gzip")
	w.Header().Set("Vary", "Accept-Encoding")
	w.WriteHeader(http.StatusOK)
	io.Copy(w, f)
}

func acceptsGzip(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept-Encoding"), "gzip")
}

// precompressedExts are the file extensions we gzip during the build.
var precompressedExts = map[string]bool{
	".js":  true,
	".css": true,
	".svg": true,
}

func isPrecompressedExt(name string) bool {
	return precompressedExts[path.Ext(name)]
}
