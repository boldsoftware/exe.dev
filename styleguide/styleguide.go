// Package styleguide serves static HTML pages that preview UI components
// in isolation. Mount the handler on the debug server to browse components
// with multiple states without needing real data.
package styleguide

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed pages
var pages embed.FS

// Handler returns an http.Handler that serves the style guide pages.
// Mount it at a path like "/debug/styleguide/".
func Handler() http.Handler {
	sub, _ := fs.Sub(pages, "pages")
	return http.FileServer(http.FS(sub))
}
