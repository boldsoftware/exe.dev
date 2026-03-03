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

func TestParseMarkdownPostEmbargo(t *testing.T) {
	const md = `---
title: Embargoed Post
description: A post with an embargo.
author: tester
date: 2024-01-02
published: true
embargo: "2024-01-10T08:00:00-08:00"
---
Body text.
`
	entry, err := parseMarkdownPost("2024-01-02-embargoed.md", []byte(md))
	if err != nil {
		t.Fatalf("parseMarkdownPost returned error: %v", err)
	}
	if entry.Embargo.IsZero() {
		t.Fatal("expected embargo to be set")
	}
	want := time.Date(2024, time.January, 10, 8, 0, 0, 0, time.FixedZone("", -8*60*60))
	if !entry.Embargo.Equal(want) {
		t.Fatalf("got embargo %v; want %v", entry.Embargo, want)
	}
	if !entry.Published {
		t.Fatal("expected entry to be published")
	}

	// Before embargo: not public.
	before := time.Date(2024, time.January, 9, 0, 0, 0, 0, time.UTC)
	if entry.IsPublic(before) {
		t.Fatal("expected entry to not be public before embargo")
	}

	// After embargo: public.
	after := time.Date(2024, time.January, 11, 0, 0, 0, 0, time.UTC)
	if !entry.IsPublic(after) {
		t.Fatal("expected entry to be public after embargo")
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
	now := time.Now()
	var first *Entry
	for _, entry := range store.Entries() {
		if entry.IsPublic(now) && !strings.Contains(body, entry.Title) {
			t.Fatalf("expected list page to mention %q", entry.Title)
		}
		if first == nil && entry.IsPublic(now) {
			first = entry
		}
	}
	if first == nil {
		t.Skip("no public blog entries available")
	}

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

	now := time.Now()
	var entry *Entry
	for _, e := range store.Entries() {
		if e.IsPublic(now) {
			entry = e
			break
		}
	}
	if entry == nil {
		t.Skip("no public blog entries available from disk")
	}
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

	// Entry request without header should redirect to login.
	req = httptest.NewRequest("GET", draft.Path, nil)
	w = httptest.NewRecorder()
	if !handler.Handle(w, req) {
		t.Fatal("expected handler to handle unpublished entry without header")
	}
	if w.Result().StatusCode != http.StatusFound {
		t.Fatalf("status = %d; want %d", w.Result().StatusCode, http.StatusFound)
	}
	wantLocation := "/__exe.dev/login?redirect=" + draft.Path
	if got := w.Result().Header.Get("Location"); got != wantLocation {
		t.Fatalf("Location = %q; want %q", got, wantLocation)
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

func TestHandlerEmbargo(t *testing.T) {
	now := time.Date(2025, time.January, 15, 12, 0, 0, 0, time.UTC)
	pub := &Entry{
		Path:      "/published-entry",
		Title:     "Published Entry",
		Author:    "Author",
		Published: true,
		Date:      now.Add(-48 * time.Hour),
	}
	embargoed := &Entry{
		Path:      "/embargoed-entry",
		Title:     "Embargoed Entry",
		Author:    "Author",
		Published: true,
		Embargo:   now.Add(24 * time.Hour), // embargo lifts tomorrow
		Date:      now.Add(-time.Hour),
	}
	store := &Store{
		entries: []*Entry{embargoed, pub},
		byPath: map[string]*Entry{
			pub.Path:       pub,
			embargoed.Path: embargoed,
		},
		defaultPath: embargoed.Path,
		updated:     now,
	}

	blogTmpl, atomTmpl := DefaultTemplates()
	handler := NewHandlerWithTemplates(store, false, blogTmpl, atomTmpl)
	if handler == nil {
		t.Fatal("expected handler")
	}
	handler.now = func() time.Time { return now }

	// List request without header should hide embargoed entry.
	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	if !handler.Handle(w, req) {
		t.Fatal("handler did not handle /")
	}
	if w.Result().StatusCode != http.StatusOK {
		t.Fatalf("status = %d; want %d", w.Result().StatusCode, http.StatusOK)
	}
	if strings.Contains(w.Body.String(), embargoed.Title) {
		t.Fatalf("expected embargoed entry %q to be hidden", embargoed.Title)
	}

	// List request with header should show embargoed entry with marker.
	req = httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-ExeDev-Email", "author@exe.dev")
	w = httptest.NewRecorder()
	if !handler.Handle(w, req) {
		t.Fatal("handler did not handle / with header")
	}
	if w.Result().StatusCode != http.StatusOK {
		t.Fatalf("status = %d; want %d", w.Result().StatusCode, http.StatusOK)
	}
	if !strings.Contains(w.Body.String(), embargoed.Title) {
		t.Fatalf("expected embargoed entry %q to be visible", embargoed.Title)
	}
	if !strings.Contains(w.Body.String(), "[embargoed]") {
		t.Fatal("expected embargoed marker in list output")
	}

	// Entry request without header should redirect to login.
	req = httptest.NewRequest("GET", embargoed.Path, nil)
	w = httptest.NewRecorder()
	if !handler.Handle(w, req) {
		t.Fatal("expected handler to handle embargoed entry without header")
	}
	if w.Result().StatusCode != http.StatusFound {
		t.Fatalf("status = %d; want %d", w.Result().StatusCode, http.StatusFound)
	}

	// Entry request with header should render with embargoed marker.
	req = httptest.NewRequest("GET", embargoed.Path, nil)
	req.Header.Set("X-ExeDev-Email", "author@exe.dev")
	w = httptest.NewRecorder()
	if !handler.Handle(w, req) {
		t.Fatal("handler did not handle embargoed entry with header")
	}
	if w.Result().StatusCode != http.StatusOK {
		t.Fatalf("status = %d; want %d", w.Result().StatusCode, http.StatusOK)
	}
	if !strings.Contains(w.Body.String(), "[embargoed]") {
		t.Fatal("expected embargoed marker on entry page")
	}

	// After embargo lifts, entry should be public.
	handler.now = func() time.Time { return now.Add(48 * time.Hour) }
	req = httptest.NewRequest("GET", "/", nil)
	w = httptest.NewRecorder()
	if !handler.Handle(w, req) {
		t.Fatal("handler did not handle /")
	}
	if !strings.Contains(w.Body.String(), embargoed.Title) {
		t.Fatal("expected entry to be visible after embargo lifts")
	}
}
