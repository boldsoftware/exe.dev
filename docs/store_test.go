package docs

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"exe.dev/stage"
)

func TestParseMarkdownDocStripsFrontMatter(t *testing.T) {
	const markdown = `---
title: Example Doc
description: short desc
---
Hello **world**!
`

	entry, err := parseMarkdownDoc("example-doc.md", []byte(markdown))
	if err != nil {
		t.Fatalf("parseMarkdownDoc returned error: %v", err)
	}

	if entry.Markdown != "Hello **world**!\n" {
		t.Fatalf("unexpected markdown body: %q", entry.Markdown)
	}
}

func TestHandlerDocsRedirect(t *testing.T) {
	store, err := Load(stage.Prod())
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	handler := NewHandler(store, false)
	if handler == nil {
		t.Fatal("NewHandler returned nil")
	}

	tests := []struct {
		name           string
		path           string
		wantRedirect   bool
		wantStatusCode int
		wantLocation   string
	}{
		{
			name:           "/docs redirects to first doc",
			path:           "/docs",
			wantRedirect:   true,
			wantStatusCode: http.StatusTemporaryRedirect,
			wantLocation:   store.DefaultPath(),
		},
		{
			name:           "/docs/ redirects to first doc",
			path:           "/docs/",
			wantRedirect:   true,
			wantStatusCode: http.StatusTemporaryRedirect,
			wantLocation:   store.DefaultPath(),
		},
		{
			name:           "/docs/list shows TOC",
			path:           "/docs/list",
			wantRedirect:   false,
			wantStatusCode: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", tt.path, nil)
			w := httptest.NewRecorder()

			handled := handler.Handle(w, req)
			if !handled {
				t.Fatalf("Handler did not handle %s", tt.path)
			}

			resp := w.Result()
			if resp.StatusCode != tt.wantStatusCode {
				t.Errorf("got status code %d, want %d", resp.StatusCode, tt.wantStatusCode)
			}

			if tt.wantRedirect {
				location := resp.Header.Get("Location")
				if location != tt.wantLocation {
					t.Errorf("got redirect location %q, want %q", location, tt.wantLocation)
				}
			}
		})
	}
}

func TestHandlerDocsAllMd(t *testing.T) {
	store, err := Load(stage.Local())
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	handler := NewHandler(store, true)
	if handler == nil {
		t.Fatal("NewHandler returned nil")
	}

	req := httptest.NewRequest("GET", "/docs/all.md", nil)
	w := httptest.NewRecorder()

	handled := handler.Handle(w, req)
	if !handled {
		t.Fatal("Handler did not handle /docs/all.md")
	}

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("got status code %d, want %d", resp.StatusCode, http.StatusOK)
	}

	contentType := resp.Header.Get("Content-Type")
	if contentType != "text/markdown; charset=utf-8" {
		t.Errorf("got content type %q, want %q", contentType, "text/markdown; charset=utf-8")
	}

	body := w.Body.String()
	if body == "" {
		t.Fatal("response body is empty")
	}

	// Verify that all published, linked docs are included
	for _, entry := range store.entries {
		if !entry.Published || entry.Unlinked {
			continue
		}
		if entry.Markdown == "" {
			continue
		}
		// Check that the markdown content appears in the combined output
		line := entry.Markdown[:min(len(entry.Markdown), 50)]
		if !contains(body, line) {
			t.Errorf("expected to find content from %s in all.md output", entry.Slug)
		}
	}
}

func TestParsePreviewFrontMatter(t *testing.T) {
	entry, err := parseMarkdownDoc("test.md", []byte(`---
title: Preview Doc
preview: true
---
Body.
`))
	if err != nil {
		t.Fatal(err)
	}
	if !entry.Preview {
		t.Fatal("expected Preview to be true")
	}
	if entry.Published {
		t.Fatal("expected Published to be false when preview is set")
	}
	if !entry.Visible() {
		t.Fatal("expected preview entry to be Visible")
	}
}

func TestParsePreviewDoesNotOverrideExplicitPublished(t *testing.T) {
	// Even if published: true appears before preview: true, preview wins.
	entry, err := parseMarkdownDoc("test.md", []byte(`---
title: Test
published: true
preview: true
---
Body.
`))
	if err != nil {
		t.Fatal(err)
	}
	if entry.Published {
		t.Fatal("expected preview: true to force Published=false")
	}
	if !entry.Preview {
		t.Fatal("expected Preview to be true")
	}
}

func TestEntryVisibility(t *testing.T) {
	tests := []struct {
		name    string
		entry   Entry
		visible bool
	}{
		{"published", Entry{Published: true}, true},
		{"preview", Entry{Preview: true}, true},
		{"draft", Entry{}, false},
		{"preview not published", Entry{Preview: true, Published: false}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.entry.Visible(); got != tt.visible {
				t.Errorf("Visible() = %v, want %v", got, tt.visible)
			}
		})
	}
}

var previewTestFS = fstest.MapFS{
	"content/published.md": &fstest.MapFile{Data: []byte(`---
title: Published Doc
subheading: Docs
published: true
---
Published body.
`)},
	"content/preview.md": &fstest.MapFile{Data: []byte(`---
title: Preview Doc
subheading: Docs
preview: true
---
Preview body.
`)},
	"content/draft.md": &fstest.MapFile{Data: []byte(`---
title: Draft Doc
subheading: Docs
published: false
---
Draft body.
`)},
}

func TestPreviewDocLoadedWhenEnabled(t *testing.T) {
	env := stage.Prod()
	env.ShowDocsPreview = true
	store, err := loadFromFS(previewTestFS, env)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := store.EntryBySlug("preview"); !ok {
		t.Fatal("preview doc should be loaded when ShowDocsPreview is true")
	}
}

func TestPreviewDocNotLoadedInProd(t *testing.T) {
	store, err := loadFromFS(previewTestFS, stage.Prod())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := store.EntryBySlug("preview"); ok {
		t.Fatal("preview doc should not be loaded in Prod")
	}
	// Published doc should still be there.
	if _, ok := store.EntryBySlug("published"); !ok {
		t.Fatal("published doc should be loaded in Prod")
	}
}

func TestPreviewDocNotInSitemapEntries(t *testing.T) {
	env := stage.Prod()
	env.ShowDocsPreview = true
	store, err := loadFromFS(previewTestFS, env)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range store.Entries() {
		if entry.Slug == "preview" {
			t.Fatal("preview doc should not appear in sitemap Entries()")
		}
	}
}

func TestPreviewDocVisibleInRenderedOutput(t *testing.T) {
	env := stage.Prod()
	env.ShowDocsPreview = true
	store, err := loadFromFS(previewTestFS, env)
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(store, false)

	req := httptest.NewRequest("GET", "/docs/all.md", nil)
	w := httptest.NewRecorder()
	if !handler.Handle(w, req) {
		t.Fatal("handler did not handle /docs/all.md")
	}
	body := w.Body.String()
	if !strings.Contains(body, "Preview body") {
		t.Error("preview doc content should appear in rendered output")
	}
	if strings.Contains(body, "Draft body") {
		t.Error("draft doc content should not appear when showHidden is false")
	}
}

func TestDraftDocNotLoadedWithoutShowHidden(t *testing.T) {
	env := stage.Prod()
	env.ShowDocsPreview = true
	store, err := loadFromFS(previewTestFS, env)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := store.EntryBySlug("draft"); ok {
		t.Fatal("draft doc should not be loaded without ShowHiddenDocs")
	}
}

func TestStatusTag(t *testing.T) {
	showHidden := NewHandler(&Store{}, true)
	noShowHidden := NewHandler(&Store{}, false)

	tests := []struct {
		name    string
		handler *Handler
		entry   *Entry
		want    string
	}{
		{"published", noShowHidden, &Entry{Published: true}, ""},
		{"preview", noShowHidden, &Entry{Preview: true}, " [preview]"},
		{"draft with showHidden", showHidden, &Entry{}, " [draft]"},
		{"draft without showHidden", noShowHidden, &Entry{}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.handler.statusTag(tt.entry); got != tt.want {
				t.Errorf("statusTag() = %q, want %q", got, tt.want)
			}
		})
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		(len(s) > 0 && (s[:len(substr)] == substr || contains(s[1:], substr))))
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func TestHandlerDocsMdIndex(t *testing.T) {
	store, err := Load(stage.Local())
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	handler := NewHandler(store, true)
	if handler == nil {
		t.Fatal("NewHandler returned nil")
	}

	req := httptest.NewRequest("GET", "/docs.md", nil)
	w := httptest.NewRecorder()

	handled := handler.Handle(w, req)
	if !handled {
		t.Fatal("Handler did not handle /docs.md")
	}

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("got status code %d, want %d", resp.StatusCode, http.StatusOK)
	}

	contentType := resp.Header.Get("Content-Type")
	if contentType != "text/markdown; charset=utf-8" {
		t.Errorf("got content type %q, want %q", contentType, "text/markdown; charset=utf-8")
	}

	body := w.Body.String()
	if body == "" {
		t.Fatal("response body is empty")
	}

	// Check that it starts with the expected header
	if !contains(body, "# exe.dev docs") {
		t.Error("expected body to contain '# exe.dev docs' header")
	}

	// Verify that all published, linked doc titles are listed
	for _, entry := range store.entries {
		if !entry.Published || entry.Unlinked {
			continue
		}
		// Check that the entry title appears as a markdown link
		if !contains(body, entry.Title) {
			t.Errorf("expected to find title %q in docs.md output", entry.Title)
		}
	}
}

func TestHandlerLLMsTxt(t *testing.T) {
	store, err := Load(stage.Local())
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	handler := NewHandler(store, true)
	if handler == nil {
		t.Fatal("NewHandler returned nil")
	}

	// Get expected content from /docs/all.md
	allMdReq := httptest.NewRequest("GET", "/docs/all.md", nil)
	allMdW := httptest.NewRecorder()
	handler.Handle(allMdW, allMdReq)
	expectedBody := allMdW.Body.String()

	// Test both /llms.txt and /llms-full.txt serve the same content
	for _, path := range []string{"/llms.txt", "/llms-full.txt"} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest("GET", path, nil)
			w := httptest.NewRecorder()

			handled := handler.Handle(w, req)
			if !handled {
				t.Fatalf("Handler did not handle %s", path)
			}

			resp := w.Result()
			if resp.StatusCode != http.StatusOK {
				t.Errorf("got status code %d, want %d", resp.StatusCode, http.StatusOK)
			}

			contentType := resp.Header.Get("Content-Type")
			if contentType != "text/markdown; charset=utf-8" {
				t.Errorf("got content type %q, want %q", contentType, "text/markdown; charset=utf-8")
			}

			body := w.Body.String()
			if body != expectedBody {
				t.Errorf("%s content differs from /docs/all.md", path)
			}
		})
	}
}
