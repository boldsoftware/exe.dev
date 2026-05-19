// Package flue serves the exe.dev Flue connector install doc and source
// from the exed binary. The two files are the public artifacts that an AI
// coding agent fetches when installing the connector into a Flue project:
//
//	curl -fsSL https://exe.dev/flue/exedev.md | claude
//
// The .md is a prompt; it tells the agent to curl the .ts and write it
// into .flue/connectors/exedev.ts.
//
// The .md contains a `{{BASE_URL}}` placeholder that is substituted at
// request time with the scheme + host + /flue prefix from the request,
// so the install doc is stage-agnostic — it works against localhost:8080
// in dev, exe.dev in prod, or any future canonical host.
package flue

import (
	"bytes"
	"embed"
	"net/http"
	"strings"
)

//go:embed exedev.md exedev.ts
var assets embed.FS

// Handle serves /flue/exedev.md and /flue/exedev.ts. Returns true if the
// request was handled.
func Handle(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	name := strings.TrimPrefix(r.URL.Path, "/flue/")
	if name == r.URL.Path || name == "" {
		return false
	}
	data, err := assets.ReadFile(name)
	if err != nil {
		return false
	}
	switch {
	case strings.HasSuffix(name, ".md"):
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		data = bytes.ReplaceAll(data, []byte("{{BASE_URL}}"), []byte(baseURL(r)))
	case strings.HasSuffix(name, ".ts"):
		w.Header().Set("Content-Type", "text/x-typescript; charset=utf-8")
	}
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.Write(data)
	return true
}

// baseURL builds the canonical URL for fetching sibling assets, derived
// from the incoming request. Returns e.g. "https://exe.dev/flue" or
// "http://localhost:8080/flue".
func baseURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	return scheme + "://" + r.Host + "/flue"
}
