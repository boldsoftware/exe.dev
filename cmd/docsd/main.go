// Command docsd is a lightweight docs dev server with live reload.
// Edit markdown files in docs/content/ and refresh the browser to see changes.
package main

import (
	"flag"
	"log"
	"net/http"
	"os"
	"path/filepath"

	docspkg "exe.dev/docs"
	"exe.dev/stage"
)

func main() {
	addr := flag.String("http", ":8081", "HTTP listen address")
	flag.Parse()

	repoRoot := findRepoRoot()
	docsDir := filepath.Join(repoRoot, "docs")
	topbarPath := filepath.Join(repoRoot, "templates", "topbar.html")
	staticDir := filepath.Join(repoRoot, "execore", "static")

	env := stage.Local()

	// Verify initial load succeeds.
	if _, err := docspkg.LoadFromDir(docsDir, env); err != nil {
		log.Fatalf("initial docs load: %v", err)
	}
	if _, err := docspkg.ParseTemplatesFromDir(docsDir, topbarPath); err != nil {
		log.Fatalf("initial template parse: %v", err)
	}

	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir(staticDir))))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.Redirect(w, r, "/docs", http.StatusTemporaryRedirect)
			return
		}
		store, err := docspkg.LoadFromDir(docsDir, env)
		if err != nil {
			log.Printf("docs load error: %v", err)
			http.Error(w, "docs load error: "+err.Error(), http.StatusInternalServerError)
			return
		}
		tmpl, err := docspkg.ParseTemplatesFromDir(docsDir, topbarPath)
		if err != nil {
			log.Printf("template parse error: %v", err)
			http.Error(w, "template parse error: "+err.Error(), http.StatusInternalServerError)
			return
		}
		handler := docspkg.NewHandlerWithTemplates(store, true, tmpl)
		if !handler.Handle(w, r) {
			http.NotFound(w, r)
		}
	})

	log.Printf("docs dev server: http://localhost%s/docs", *addr)
	log.Fatal(http.ListenAndServe(*addr, mux))
}

func findRepoRoot() string {
	wd, err := os.Getwd()
	if err != nil {
		log.Fatalf("getwd: %v", err)
	}
	dir := wd
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			log.Fatal("not in a git repository")
		}
		dir = parent
	}
}
