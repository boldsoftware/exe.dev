package docs

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/yuin/goldmark"
	gmmeta "github.com/yuin/goldmark-meta"
	"github.com/yuin/goldmark/parser"
	htmlrenderer "github.com/yuin/goldmark/renderer/html"
)

var loadTime = time.Now()

//go:embed content
var contentFS embed.FS

//go:embed doc-entry.html docs-list.html
var templateFS embed.FS

var docTemplates = template.Must(template.New("docs").ParseFS(templateFS, "doc-entry.html", "docs-list.html"))

var markdown = goldmark.New(
	goldmark.WithExtensions(gmmeta.Meta),
	goldmark.WithRendererOptions(htmlrenderer.WithUnsafe()),
)

type Asset struct {
	Data    []byte
	ModTime time.Time
}

type Entry struct {
	Path        string
	Slug        string
	Markdown    string
	Author      string
	Title       string
	Description string
	Subheading  string
	Suborder    int
	Tags        []string
	Published   bool
	Content     template.HTML
}

type Group struct {
	Heading string
	Docs    []*Entry
}

type Store struct {
	entries     []*Entry
	groups      []Group
	byPath      map[string]*Entry
	bySlug      map[string]*Entry
	assets      map[string]Asset
	defaultPath string
}

func Load(includeUnpublished bool) (*Store, error) {
	store := &Store{
		byPath: make(map[string]*Entry),
		bySlug: make(map[string]*Entry),
		assets: make(map[string]Asset),
	}

	if _, err := fs.ReadDir(contentFS, "content"); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return store, nil
		}
		return nil, fmt.Errorf("reading docs content directory: %w", err)
	}

	err := fs.WalkDir(contentFS, "content", func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		if d.IsDir() {
			return nil
		}

		data, err := contentFS.ReadFile(path)
		if err != nil {
			return fmt.Errorf("reading %s: %w", path, err)
		}

		rel := strings.TrimPrefix(path, "content/")
		if rel == path {
			return fmt.Errorf("unexpected docs path: %s", path)
		}

		switch {
		case strings.HasSuffix(rel, ".md"):
			entry, err := parseMarkdownDoc(rel, data)
			if err != nil {
				return fmt.Errorf("parsing %s: %w", path, err)
			}
			if entry.Published || includeUnpublished {
				copyEntry := entry
				store.entries = append(store.entries, &copyEntry)
			}
			return nil
		default:
			assetPath := "/docs/" + rel
			store.assets[assetPath] = Asset{Data: data, ModTime: loadTime}
			return nil
		}
	})
	if err != nil {
		return nil, err
	}

	sort.Slice(store.entries, func(i, j int) bool {
		a, b := store.entries[i], store.entries[j]
		if a.Published != b.Published {
			return a.Published
		}
		if a.Subheading != b.Subheading {
			return a.Subheading < b.Subheading
		}
		if a.Suborder != 0 && b.Suborder != 0 && a.Suborder != b.Suborder {
			return a.Suborder < b.Suborder
		}
		if a.Suborder != 0 && b.Suborder == 0 {
			return true
		}
		if a.Suborder == 0 && b.Suborder != 0 {
			return false
		}
		return a.Title < b.Title
	})

	store.groups = groupDocsByHeading(store.entries)

	for _, entry := range store.entries {
		store.byPath[entry.Path] = entry
		if entry.Slug != "" {
			store.bySlug[entry.Slug] = entry
		}
	}

	for _, entry := range store.entries {
		if entry.Published {
			store.defaultPath = entry.Path
			break
		}
	}
	if store.defaultPath == "" && len(store.entries) > 0 {
		store.defaultPath = store.entries[0].Path
	}

	return store, nil
}

func (s *Store) DefaultPath() string {
	return s.defaultPath
}

func (s *Store) Groups() []Group {
	return s.groups
}

func (s *Store) Entry(path string) (*Entry, bool) {
	entry, ok := s.byPath[path]
	return entry, ok
}

func (s *Store) Asset(path string) (Asset, bool) {
	asset, ok := s.assets[path]
	return asset, ok
}

func (s *Store) EntryBySlug(slug string) (*Entry, bool) {
	if s == nil {
		return nil, false
	}
	entry, ok := s.bySlug[slug]
	return entry, ok
}

func (s *Store) Slugs() []string {
	if s == nil {
		return nil
	}
	slugs := make([]string, 0, len(s.bySlug))
	for slug := range s.bySlug {
		slugs = append(slugs, slug)
	}
	sort.Strings(slugs)
	return slugs
}

type Handler struct {
	store      *Store
	showHidden bool
}

func NewHandler(store *Store, showHidden bool) *Handler {
	if store == nil {
		return nil
	}
	return &Handler{store: store, showHidden: showHidden}
}

func (h *Handler) Store() *Store {
	if h == nil {
		return nil
	}
	return h.store
}

func (h *Handler) Handle(w http.ResponseWriter, r *http.Request) bool {
	if h == nil || h.store == nil {
		return false
	}

	path := r.URL.Path

	if path == "/docs" || path == "/docs/" {
		h.renderDocsList(w, r)
		return true
	}

	if strings.HasSuffix(path, "/") && path != "/docs/" {
		trimmed := strings.TrimSuffix(path, "/")
		http.Redirect(w, r, trimmed, http.StatusMovedPermanently)
		return true
	}

	if entry, ok := h.store.Entry(path); ok {
		h.renderDocEntry(w, r, entry)
		return true
	}

	if path == "/docs/list" {
		h.renderDocsList(w, r)
		return true
	}

	if asset, ok := h.store.Asset(path); ok {
		http.ServeContent(w, r, path, asset.ModTime, bytes.NewReader(asset.Data))
		return true
	}

	return false
}

func (h *Handler) renderDocEntry(w http.ResponseWriter, r *http.Request, entry *Entry) {
	buf := new(bytes.Buffer)
	data := map[string]any{
		"Entry":      entry,
		"Groups":     h.store.Groups(),
		"ShowHidden": h.showHidden,
	}

	if err := docTemplates.ExecuteTemplate(buf, "doc-entry.html", data); err != nil {
		http.Error(w, "error rendering doc", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(buf.Bytes())
}

func (h *Handler) renderDocsList(w http.ResponseWriter, r *http.Request) {
	buf := new(bytes.Buffer)
	data := map[string]any{
		"Groups":     h.store.Groups(),
		"ShowHidden": h.showHidden,
	}

	if err := docTemplates.ExecuteTemplate(buf, "docs-list.html", data); err != nil {
		http.Error(w, "error rendering docs", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(buf.Bytes())
}

func groupDocsByHeading(entries []*Entry) []Group {
	groupMap := make(map[string][]*Entry)
	var headingOrder []string
	headingsSeen := make(map[string]bool)

	for _, entry := range entries {
		heading := extractMainHeading(entry.Subheading)
		if !headingsSeen[heading] {
			headingOrder = append(headingOrder, heading)
			headingsSeen[heading] = true
		}
		groupMap[heading] = append(groupMap[heading], entry)
	}

	groups := make([]Group, 0, len(headingOrder))
	for _, heading := range headingOrder {
		groups = append(groups, Group{
			Heading: heading,
			Docs:    groupMap[heading],
		})
	}
	return groups
}

func extractMainHeading(subheading string) string {
	if subheading == "" {
		return "Other"
	}
	return subheading
}

func parseMarkdownDoc(relPath string, data []byte) (Entry, error) {
	slug := strings.TrimSuffix(relPath, ".md")
	if slug == relPath {
		return Entry{}, fmt.Errorf("markdown doc missing .md extension: %s", relPath)
	}
	docPath := "/docs/" + slug
	ctx := parser.NewContext()
	var buf bytes.Buffer
	if err := markdown.Convert(data, &buf, parser.WithContext(ctx)); err != nil {
		return Entry{}, fmt.Errorf("rendering markdown: %w", err)
	}
	metadata := gmmeta.Get(ctx)
	if metadata == nil {
		return Entry{}, fmt.Errorf("missing front matter metadata in %s", relPath)
	}
	entry, err := entryFromMetadata(docPath, metadata)
	if err != nil {
		return Entry{}, err
	}
	entry.Slug = slug
	entry.Markdown = string(data)
	entry.Content = template.HTML(buf.String())
	return entry, nil
}

func entryFromMetadata(docPath string, metadata map[string]any) (Entry, error) {
	entry := Entry{
		Path:      docPath,
		Published: true,
		Tags:      make([]string, 0),
	}

	title, err := metadataString(metadata, "title", true)
	if err != nil {
		return Entry{}, err
	}
	entry.Title = title

	description, err := metadataString(metadata, "description", false)
	if err != nil {
		return Entry{}, err
	}
	entry.Description = description

	subheading, err := metadataString(metadata, "subheading", false)
	if err != nil {
		return Entry{}, err
	}
	entry.Subheading = subheading

	if raw, ok := metadata["suborder"]; ok {
		order, err := metadataInt(raw, "suborder")
		if err != nil {
			return Entry{}, err
		}
		entry.Suborder = order
	}

	if raw, ok := metadata["tags"]; ok {
		tags, err := metadataTags(raw)
		if err != nil {
			return Entry{}, err
		}
		entry.Tags = tags
	}

	if raw, ok := metadata["published"]; ok {
		published, err := metadataBool(raw, "published")
		if err != nil {
			return Entry{}, err
		}
		entry.Published = published
	}

	author, err := metadataString(metadata, "author", false)
	if err != nil {
		return Entry{}, err
	}
	entry.Author = author

	return entry, nil
}

func metadataString(metadata map[string]any, key string, required bool) (string, error) {
	raw, ok := metadata[key]
	if !ok || raw == nil {
		if required {
			return "", fmt.Errorf("missing required front matter field: %s", key)
		}
		return "", nil
	}

	val, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("front matter field %s must be a string", key)
	}
	val = strings.TrimSpace(val)
	if required && val == "" {
		return "", fmt.Errorf("front matter field %s cannot be empty", key)
	}
	return val, nil
}

func metadataInt(raw any, key string) (int, error) {
	switch v := raw.(type) {
	case int:
		return v, nil
	case int64:
		return int(v), nil
	case float64:
		if float64(int(v)) != v {
			return 0, fmt.Errorf("front matter field %s must be an integer", key)
		}
		return int(v), nil
	case string:
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			return 0, fmt.Errorf("front matter field %s cannot be empty", key)
		}
		n, err := strconv.Atoi(trimmed)
		if err != nil {
			return 0, fmt.Errorf("front matter field %s must be an integer", key)
		}
		return n, nil
	default:
		return 0, fmt.Errorf("front matter field %s must be an integer", key)
	}
}

func metadataBool(raw any, key string) (bool, error) {
	switch v := raw.(type) {
	case bool:
		return v, nil
	case string:
		s := strings.TrimSpace(strings.ToLower(v))
		switch s {
		case "true":
			return true, nil
		case "false":
			return false, nil
		}
	}
	return false, fmt.Errorf("front matter field %s must be a boolean", key)
}

func metadataTags(raw any) ([]string, error) {
	switch v := raw.(type) {
	case []string:
		return v, nil
	case []any:
		tags := make([]string, 0, len(v))
		for _, item := range v {
			s, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("front matter field tags must be a list of strings")
			}
			s = strings.TrimSpace(s)
			if s != "" {
				tags = append(tags, s)
			}
		}
		return tags, nil
	case string:
		return parseTags(v), nil
	default:
		return nil, fmt.Errorf("front matter field tags must be a string or list of strings")
	}
}

func parseTags(content string) (tags []string) {
	for _, tag := range strings.Split(content, ",") {
		tag = strings.TrimSpace(tag)
		if tag != "" {
			tags = append(tags, tag)
		}
	}
	return tags
}
