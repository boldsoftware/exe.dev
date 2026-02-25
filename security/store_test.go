package security

import (
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestParseBulletin(t *testing.T) {
	const md = `---
title: Test Bulletin
description: A test security bulletin.
author: tester
date: 2026-03-15
severity: high
published: false
---
Some vulnerability details.
`

	b, err := parseBulletin("2026-03-15-test-bulletin.md", []byte(md))
	if err != nil {
		t.Fatalf("parseBulletin: %v", err)
	}
	if b.Title != "Test Bulletin" {
		t.Fatalf("title = %q; want %q", b.Title, "Test Bulletin")
	}
	if b.Severity != "high" {
		t.Fatalf("severity = %q; want %q", b.Severity, "high")
	}
	if b.Published {
		t.Fatal("expected unpublished")
	}
	if b.DateString != "2026-03-15" {
		t.Fatalf("date = %q; want %q", b.DateString, "2026-03-15")
	}
	if b.Slug != "test-bulletin" {
		t.Fatalf("slug = %q; want %q", b.Slug, "test-bulletin")
	}
	if b.Path != "/test-bulletin" {
		t.Fatalf("path = %q; want %q", b.Path, "/test-bulletin")
	}
}

func TestLoadEmbedded(t *testing.T) {
	store, err := Load(false)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(store.Entries()) == 0 {
		t.Skip("no published security bulletins")
	}
	for _, b := range store.Entries() {
		if !b.Published {
			t.Fatalf("unpublished bulletin %q in published-only store", b.Title)
		}
	}
}

func TestLoadIncludesUnpublished(t *testing.T) {
	store, err := Load(true)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(store.Entries()) == 0 {
		t.Fatal("expected at least one bulletin with includeUnpublished=true")
	}
}

func TestHandlerList(t *testing.T) {
	store, err := Load(true)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	h := NewHandler(store, true)
	if h == nil {
		t.Fatal("nil handler")
	}

	req := httptest.NewRequest("GET", "/security", nil)
	w := httptest.NewRecorder()
	if !h.Handle(w, req) {
		t.Fatal("handler did not handle /security")
	}
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; want %d", w.Code, http.StatusOK)
	}
	body := w.Body.String()
	for _, b := range store.Entries() {
		if !strings.Contains(body, b.Title) {
			t.Fatalf("list page missing title %q", b.Title)
		}
	}
}

func TestHandlerEntry(t *testing.T) {
	store, err := Load(true)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(store.Entries()) == 0 {
		t.Skip("no bulletins")
	}
	h := NewHandler(store, true)

	first := store.Entries()[0]
	req := httptest.NewRequest("GET", "/security"+first.Path, nil)
	w := httptest.NewRecorder()
	if !h.Handle(w, req) {
		t.Fatalf("handler did not handle /security%s", first.Path)
	}
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; want %d", w.Code, http.StatusOK)
	}
	if !strings.Contains(w.Body.String(), first.Title) {
		t.Fatalf("entry page missing title %q", first.Title)
	}
}

func TestHandlerAtomFeed(t *testing.T) {
	store, err := Load(false)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	h := NewHandler(store, false)

	req := httptest.NewRequest("GET", "/security/atom.xml", nil)
	w := httptest.NewRecorder()
	if !h.Handle(w, req) {
		t.Fatal("handler did not handle /security/atom.xml")
	}
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; want %d", w.Code, http.StatusOK)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/atom+xml; charset=utf-8" {
		t.Fatalf("content-type = %q", ct)
	}

	var feed any
	if err := xml.Unmarshal(w.Body.Bytes(), &feed); err != nil {
		t.Fatalf("atom.xml is not valid XML: %v", err)
	}
}

func TestHandlerUnpublishedVisibility(t *testing.T) {
	now := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	pub := &Bulletin{
		Path:      "/published",
		Title:     "Published Bulletin",
		Author:    "tester",
		Published: true,
		Date:      now,
	}
	draft := &Bulletin{
		Path:      "/draft",
		Title:     "Draft Bulletin",
		Author:    "tester",
		Published: false,
		Date:      now.Add(-time.Hour),
	}
	store := &Store{
		entries: []*Bulletin{pub, draft},
		byPath: map[string]*Bulletin{
			pub.Path:   pub,
			draft.Path: draft,
		},
		bySlug:  make(map[string]*Bulletin),
		updated: now,
	}

	h := NewHandler(store, false)

	// Anonymous list should hide draft.
	req := httptest.NewRequest("GET", "/security", nil)
	w := httptest.NewRecorder()
	h.Handle(w, req)
	if strings.Contains(w.Body.String(), draft.Title) {
		t.Fatal("draft visible to anonymous user")
	}

	// Privileged list should show draft.
	req = httptest.NewRequest("GET", "/security", nil)
	req.Header.Set("X-ExeDev-Email", "author@exe.dev")
	w = httptest.NewRecorder()
	h.Handle(w, req)
	if !strings.Contains(w.Body.String(), draft.Title) {
		t.Fatal("draft hidden from privileged user")
	}
	if !strings.Contains(w.Body.String(), "[unpublished]") {
		t.Fatal("missing unpublished marker")
	}

	// Anonymous access to draft should redirect to login.
	req = httptest.NewRequest("GET", "/security/draft", nil)
	w = httptest.NewRecorder()
	h.Handle(w, req)
	if w.Code != http.StatusFound {
		t.Fatalf("status = %d; want %d", w.Code, http.StatusFound)
	}
	wantLocation := "/__exe.dev/login?redirect=/security/draft"
	if got := w.Header().Get("Location"); got != wantLocation {
		t.Fatalf("Location = %q; want %q", got, wantLocation)
	}

	// Privileged access to draft should render.
	req = httptest.NewRequest("GET", "/security/draft", nil)
	req.Header.Set("X-ExeDev-Email", "author@exe.dev")
	w = httptest.NewRecorder()
	h.Handle(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; want %d", w.Code, http.StatusOK)
	}
	if !strings.Contains(w.Body.String(), draft.Title) {
		t.Fatal("draft content not rendered for privileged user")
	}
}

func TestHandlerTrailingSlashRedirect(t *testing.T) {
	store := &Store{
		byPath: make(map[string]*Bulletin),
		bySlug: make(map[string]*Bulletin),
	}
	h := NewHandler(store, false)

	req := httptest.NewRequest("GET", "/security/some-bulletin/", nil)
	w := httptest.NewRecorder()
	if !h.Handle(w, req) {
		t.Fatal("handler did not handle trailing slash")
	}
	if w.Code != http.StatusMovedPermanently {
		t.Fatalf("status = %d; want %d", w.Code, http.StatusMovedPermanently)
	}
	if got := w.Header().Get("Location"); got != "/security/some-bulletin" {
		t.Fatalf("Location = %q; want /security/some-bulletin", got)
	}
}

func TestHandlerNotFound(t *testing.T) {
	store := &Store{
		byPath: make(map[string]*Bulletin),
		bySlug: make(map[string]*Bulletin),
	}
	h := NewHandler(store, false)

	req := httptest.NewRequest("GET", "/security/nonexistent", nil)
	w := httptest.NewRecorder()
	if h.Handle(w, req) {
		t.Fatal("handler should return false for unknown path")
	}
}
