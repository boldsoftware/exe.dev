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

func TestHandlerDocsAllMd(t *testing.T) {
	store, err := Load(true)
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

	// Verify that all published docs are included
	for _, entry := range store.entries {
		if !entry.Published {
			continue
		}
		if entry.Markdown == "" {
			continue
		}
		// Check that the markdown content appears in the combined output
		// We can't check for exact match due to separators, but we can check for a unique line
		lines := []string{}
		for _, line := range []string{entry.Markdown[:min(len(entry.Markdown), 50)]} {
			lines = append(lines, line)
		}
		found := false
		for _, line := range lines {
			if len(line) > 0 && contains(body, line) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected to find content from %s in all.md output", entry.Slug)
		}
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
