package docs

import (
	"net/http"
	"net/http/httptest"
	"testing"
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
	store, err := Load(false)
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
