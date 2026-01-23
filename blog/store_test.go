package blog

import (
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestParseMarkdownPostRespectsMetadata(t *testing.T) {
	const markdown = `---
title: Example Post
description: An example post for testing.
author: tester
date: 2024-01-02
tags:
  - go
published: false
---
Hello from the post body.
`

	entry, err := parseMarkdownPost("2024-01-02-example-post.md", []byte(markdown))
	if err != nil {
		t.Fatalf("parseMarkdownPost returned error: %v", err)
	}

	if entry.Title != "Example Post" {
		t.Fatalf("got title %q; want %q", entry.Title, "Example Post")
	}
	if entry.Author != "tester" {
		t.Fatalf("got author %q; want %q", entry.Author, "tester")
	}
	if entry.DateString != "2024-01-02" {
		t.Fatalf("expected ISO date, got %q", entry.DateString)
	}
	if entry.Published {
		t.Fatalf("expected entry to be unpublished")
	}
	if entry.Markdown == "" {
		t.Fatalf("expected markdown body to be stripped of front matter")
	}
}

func TestHandlerBlogListAndEntry(t *testing.T) {
	store, err := Load(false)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	handler := NewHandler(store, false)
	if handler == nil {
		t.Fatal("expected handler")
	}

	if len(store.Entries()) == 0 {
		t.Skip("no published blog entries available")
	}

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	if !handler.Handle(w, req) {
		t.Fatal("handler did not handle /")
	}
	if w.Result().StatusCode != http.StatusOK {
		t.Fatalf("status = %d; want %d", w.Result().StatusCode, http.StatusOK)
	}
	body := w.Body.String()
	for _, entry := range store.Entries() {
		if entry.Published && !strings.Contains(body, entry.Title) {
			t.Fatalf("expected list page to mention %q", entry.Title)
		}
	}

	first := store.Entries()[0]
	req = httptest.NewRequest("GET", first.Path, nil)
	w = httptest.NewRecorder()
	if !handler.Handle(w, req) {
		t.Fatalf("handler did not handle %s", first.Path)
	}
	if w.Result().StatusCode != http.StatusOK {
		t.Fatalf("status = %d; want %d", w.Result().StatusCode, http.StatusOK)
	}
	if !strings.Contains(w.Body.String(), first.Title) {
		t.Fatalf("entry page missing title %q", first.Title)
	}
}

func TestHandlerAtomFeed(t *testing.T) {
	store, err := Load(false)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	handler := NewHandler(store, false)
	if handler == nil {
		t.Fatal("expected handler")
	}

	req := httptest.NewRequest("GET", "/atom.xml", nil)
	w := httptest.NewRecorder()
	if !handler.Handle(w, req) {
		t.Fatal("handler did not handle /atom.xml")
	}
	if got := w.Result().StatusCode; got != http.StatusOK {
		t.Fatalf("status = %d; want %d", got, http.StatusOK)
	}
	if ct := w.Result().Header.Get("Content-Type"); ct != "application/atom+xml; charset=utf-8" {
		t.Fatalf("content-type = %q; want %q", ct, "application/atom+xml; charset=utf-8")
	}
	if !strings.Contains(w.Body.String(), "<feed") {
		t.Fatal("expected atom output")
	}
}

func TestAtomFeedValidXML(t *testing.T) {
	store, err := Load(false)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if len(store.Entries()) == 0 {
		t.Skip("no published blog entries available")
	}
	handler := NewHandler(store, false)
	if handler == nil {
		t.Fatal("expected handler")
	}

	req := httptest.NewRequest("GET", "/atom.xml", nil)
	w := httptest.NewRecorder()
	if !handler.Handle(w, req) {
		t.Fatal("handler did not handle /atom.xml")
	}
	if got := w.Result().StatusCode; got != http.StatusOK {
		t.Fatalf("status = %d; want %d", got, http.StatusOK)
	}

	// Validate that the atom feed parses as valid XML.
	var feed any
	if err := xml.Unmarshal(w.Body.Bytes(), &feed); err != nil {
		t.Fatalf("atom.xml is not valid XML: %v\n\nContent:\n%s", err, w.Body.String())
	}
}

func TestLoadFromDirAndTemplates(t *testing.T) {
	store, err := LoadFromDir(".", false)
	if err != nil {
		t.Fatalf("LoadFromDir returned error: %v", err)
	}
	if len(store.Entries()) == 0 {
		t.Skip("no published blog entries available from disk")
	}

	blogTmpl, atomTmpl, err := ParseTemplatesFromDir(".")
	if err != nil {
		t.Fatalf("ParseTemplatesFromDir returned error: %v", err)
	}

	handler := NewHandlerWithTemplates(store, false, blogTmpl, atomTmpl)
	if handler == nil {
		t.Fatal("expected handler")
	}

	entry := store.Entries()[0]
	req := httptest.NewRequest("GET", entry.Path, nil)
	w := httptest.NewRecorder()
	if !handler.Handle(w, req) {
		t.Fatalf("handler did not handle %s", entry.Path)
	}
	if w.Result().StatusCode != http.StatusOK {
		t.Fatalf("status = %d; want %d", w.Result().StatusCode, http.StatusOK)
	}
	if !strings.Contains(w.Body.String(), entry.Title) {
		t.Fatalf("entry page missing title %q", entry.Title)
	}
}

func TestHandlerPreviewUnpublished(t *testing.T) {
	now := time.Date(2025, time.January, 1, 0, 0, 0, 0, time.UTC)
	pub := &Entry{
		Path:      "/published-entry",
		Title:     "Published Entry",
		Author:    "Author",
		Published: true,
		Date:      now,
	}
	draft := &Entry{
		Path:      "/draft-entry",
		Title:     "Draft Entry",
		Author:    "Author",
		Published: false,
		Date:      now.Add(-time.Hour),
	}
	store := &Store{
		entries: []*Entry{pub, draft},
		byPath: map[string]*Entry{
			pub.Path:   pub,
			draft.Path: draft,
		},
		defaultPath: pub.Path,
		updated:     now,
	}

	blogTmpl, atomTmpl := DefaultTemplates()
	handler := NewHandlerWithTemplates(store, false, blogTmpl, atomTmpl)
	if handler == nil {
		t.Fatal("expected handler")
	}

	// List request without header should hide draft.
	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	if !handler.Handle(w, req) {
		t.Fatal("handler did not handle /")
	}
	if w.Result().StatusCode != http.StatusOK {
		t.Fatalf("status = %d; want %d", w.Result().StatusCode, http.StatusOK)
	}
	if strings.Contains(w.Body.String(), draft.Title) {
		t.Fatalf("expected unpublished entry %q to be hidden", draft.Title)
	}

	// List request with header should show draft label.
	req = httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-ExeDev-Email", "author@exe.dev")
	w = httptest.NewRecorder()
	if !handler.Handle(w, req) {
		t.Fatal("handler did not handle / with header")
	}
	if w.Result().StatusCode != http.StatusOK {
		t.Fatalf("status = %d; want %d", w.Result().StatusCode, http.StatusOK)
	}
	if !strings.Contains(w.Body.String(), draft.Title) {
		t.Fatalf("expected unpublished entry %q to be visible", draft.Title)
	}
	if !strings.Contains(w.Body.String(), "[unpublished]") {
		t.Fatal("expected unpublished marker in list output")
	}

	// Entry request without header should be denied.
	req = httptest.NewRequest("GET", draft.Path, nil)
	w = httptest.NewRecorder()
	if handler.Handle(w, req) {
		t.Fatal("expected handler to refuse unpublished entry without header")
	}

	// Entry request with header should render successfully.
	req = httptest.NewRequest("GET", draft.Path, nil)
	req.Header.Set("X-ExeDev-Email", "author@exe.dev")
	w = httptest.NewRecorder()
	if !handler.Handle(w, req) {
		t.Fatal("handler did not handle unpublished entry with header")
	}
	if w.Result().StatusCode != http.StatusOK {
		t.Fatalf("status = %d; want %d", w.Result().StatusCode, http.StatusOK)
	}
	if !strings.Contains(w.Body.String(), "[unpublished]") {
		t.Fatal("expected unpublished marker on entry page")
	}
}
