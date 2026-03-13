package blog

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"
	"time"

	templatespkg "exe.dev/templates"
	"github.com/yuin/goldmark"
	gmmeta "github.com/yuin/goldmark-meta"
	"github.com/yuin/goldmark/parser"
	htmlrenderer "github.com/yuin/goldmark/renderer/html"
)

var loadTime = time.Now()

//go:embed *.md
var postsFS embed.FS

//go:embed assets/*
var assetsFS embed.FS

//go:embed blog-entry.html blog-list.html atom.xml topbar.html
var templateFS embed.FS

var markdown = goldmark.New(
	goldmark.WithExtensions(gmmeta.Meta),
	goldmark.WithRendererOptions(htmlrenderer.WithUnsafe(), htmlrenderer.WithXHTML()),
	goldmark.WithRenderer(Renderer()),
)

var (
	defaultBlogTemplates = template.Must(parseBlogTemplates(templateFS))
	defaultAtomTemplate  = template.Must(parseAtomTemplate(templateFS))
)

func parseBlogTemplates(fsys fs.FS) (*template.Template, error) {
	tmpl, err := template.New("blog").ParseFS(fsys, "blog-entry.html", "blog-list.html")
	if err != nil {
		return nil, err
	}
	// Parse shared topbar first, then override with blog-specific topbar definitions.
	if _, err := tmpl.ParseFS(templatespkg.Files, "topbar.html"); err != nil {
		return nil, err
	}
	if _, err := tmpl.ParseFS(fsys, "topbar.html"); err != nil {
		return nil, err
	}
	return tmpl, nil
}

func parseAtomTemplate(fsys fs.FS) (*template.Template, error) {
	return template.New("atom.xml").ParseFS(fsys, "atom.xml")
}

// DefaultTemplates returns the embedded templates used in production.
func DefaultTemplates() (*template.Template, *template.Template) {
	return defaultBlogTemplates, defaultAtomTemplate
}

// ParseTemplatesFromFS parses blog templates from the provided filesystem.
func ParseTemplatesFromFS(fsys fs.FS) (*template.Template, *template.Template, error) {
	blogTmpl, err := parseBlogTemplates(fsys)
	if err != nil {
		return nil, nil, err
	}
	atomTmpl, err := parseAtomTemplate(fsys)
	if err != nil {
		return nil, nil, err
	}
	return blogTmpl, atomTmpl, nil
}

// ParseTemplatesFromDir parses blog templates from a directory on disk.
func ParseTemplatesFromDir(dir string) (*template.Template, *template.Template, error) {
	return ParseTemplatesFromFS(os.DirFS(dir))
}

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
	Tags        []string
	Published   bool
	Embargo     time.Time
	Date        time.Time
	DateString  string
	DateRFC3339 string
	Content     template.HTML
}

// IsPublic reports whether the entry should be visible to the public at the given time.
func (e *Entry) IsPublic(now time.Time) bool {
	if !e.Published {
		return false
	}
	if !e.Embargo.IsZero() && now.Before(e.Embargo) {
		return false
	}
	return true
}

type Store struct {
	entries     []*Entry
	byPath      map[string]*Entry
	bySlug      map[string]*Entry
	assets      map[string]Asset
	defaultPath string
	updated     time.Time
}

func Load(includeUnpublished bool) (*Store, error) {
	return loadStoreFromFS(postsFS, assetsFS, includeUnpublished, loadTime)
}

// LoadFromDir reads blog content and assets from a directory on disk.
func LoadFromDir(dir string, includeUnpublished bool) (*Store, error) {
	cleaned := filepath.Clean(dir)
	if cleaned == "" {
		cleaned = "."
	}
	dirFS := os.DirFS(cleaned)
	return loadStoreFromFS(dirFS, dirFS, includeUnpublished, time.Now())
}

func loadStoreFromFS(postsFS, assetsFS fs.FS, includeUnpublished bool, assetModTime time.Time) (*Store, error) {
	store := &Store{
		byPath: make(map[string]*Entry),
		bySlug: make(map[string]*Entry),
		assets: make(map[string]Asset),
	}

	if _, err := fs.ReadDir(postsFS, "."); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return store, nil
		}
		return nil, fmt.Errorf("reading blog content directory: %w", err)
	}

	err := fs.WalkDir(postsFS, ".", func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == "." {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(path), ".md") {
			return nil
		}
		base := filepath.Base(path)
		if len(base) < len("2006-01-02-a.md") || base[4] != '-' || base[7] != '-' || base[10] != '-' {
			return nil // skip non-post files like AGENTS.md, README.md
		}

		data, err := fs.ReadFile(postsFS, path)
		if err != nil {
			return fmt.Errorf("reading %s: %w", path, err)
		}

		entry, err := parseMarkdownPost(path, data)
		if err != nil {
			return fmt.Errorf("parsing %s: %w", path, err)
		}
		if entry.Published || includeUnpublished {
			copyEntry := entry
			store.entries = append(store.entries, &copyEntry)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	if _, err := fs.ReadDir(assetsFS, "assets"); err == nil {
		err = fs.WalkDir(assetsFS, "assets", func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.IsDir() {
				return nil
			}
			if strings.HasSuffix(path, ".keep") {
				return nil
			}
			data, err := fs.ReadFile(assetsFS, path)
			if err != nil {
				return fmt.Errorf("reading asset %s: %w", path, err)
			}
			rel := strings.TrimPrefix(path, "assets/")
			if rel == path || rel == "" {
				return fmt.Errorf("unexpected asset path: %s", path)
			}
			assetPath := "/assets/" + rel
			store.assets[assetPath] = Asset{Data: data, ModTime: assetModTime}
			return nil
		})
		if err != nil {
			return nil, err
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("reading blog assets directory: %w", err)
	}

	sort.Slice(store.entries, func(i, j int) bool {
		a, b := store.entries[i], store.entries[j]
		if !a.Date.Equal(b.Date) {
			return a.Date.After(b.Date)
		}
		return a.Title < b.Title
	})

	for _, entry := range store.entries {
		store.byPath[entry.Path] = entry
		store.bySlug[entry.Slug] = entry
		if store.updated.IsZero() || entry.Date.After(store.updated) {
			store.updated = entry.Date
		}
	}

	if len(store.entries) > 0 {
		store.defaultPath = store.entries[0].Path
	}

	return store, nil
}

func (s *Store) Entries() []*Entry {
	return s.entries
}

func (s *Store) Entry(path string) (*Entry, bool) {
	entry, ok := s.byPath[path]
	return entry, ok
}

func (s *Store) EntryBySlug(slug string) (*Entry, bool) {
	entry, ok := s.bySlug[slug]
	return entry, ok
}

func (s *Store) Asset(path string) (Asset, bool) {
	asset, ok := s.assets[path]
	return asset, ok
}

func (s *Store) DefaultPath() string {
	return s.defaultPath
}

func (s *Store) Updated() time.Time {
	return s.updated
}

type Handler struct {
	store        *Store
	showHidden   bool
	templates    *template.Template
	atomTemplate *template.Template
	metrics      *Metrics
	now          func() time.Time
}

// NewHandler creates a handler that uses the embedded templates.
func NewHandler(store *Store, showHidden bool) *Handler {
	blogTmpl, atomTmpl := DefaultTemplates()
	return NewHandlerWithTemplates(store, showHidden, blogTmpl, atomTmpl)
}

// NewHandlerWithTemplates creates a handler that uses the provided templates.
func NewHandlerWithTemplates(store *Store, showHidden bool, templates, atom *template.Template) *Handler {
	if store == nil || templates == nil || atom == nil {
		return nil
	}
	return &Handler{
		store:        store,
		showHidden:   showHidden,
		templates:    templates,
		atomTemplate: atom,
		now:          time.Now,
	}
}

func (h *Handler) shouldShowHidden(r *http.Request) bool {
	if h == nil {
		return false
	}
	if h.showHidden {
		return true
	}
	return CanPreviewUnpublished(r)
}

// CanPreviewUnpublished reports whether the request may view unpublished posts.
func CanPreviewUnpublished(r *http.Request) bool {
	if r == nil {
		return false
	}
	email := strings.TrimSpace(r.Header.Get("X-ExeDev-Email"))
	if email == "" {
		return false
	}
	email = strings.ToLower(email)
	return strings.HasSuffix(email, "@bold.dev") || strings.HasSuffix(email, "@exe.dev") || email == "david@zentus.com" ||
		email == "philip.zeyliger@gmail.com" ||
		email == "josharian@gmail.com" || email == "evan@h5t.io" || email == "ian@airs.com"
}

func (h *Handler) entries(showHidden bool) []*Entry {
	if h == nil || h.store == nil {
		return nil
	}
	all := h.store.Entries()
	if showHidden {
		return all
	}
	now := h.now()
	filtered := make([]*Entry, 0, len(all))
	for _, entry := range all {
		if entry.IsPublic(now) {
			filtered = append(filtered, entry)
		}
	}
	return filtered
}

func (h *Handler) Store() *Store {
	if h == nil {
		return nil
	}
	return h.store
}

// WithMetrics attaches metrics recording to the handler.
func (h *Handler) WithMetrics(metrics *Metrics) *Handler {
	if h == nil {
		return h
	}
	h.metrics = metrics
	return h
}

func (h *Handler) Handle(w http.ResponseWriter, r *http.Request) bool {
	if h == nil || h.store == nil {
		return false
	}

	showHidden := h.shouldShowHidden(r)
	path := r.URL.Path

	if path == "" {
		path = "/"
	}

	if path == "/" {
		h.recordPageHit(path)
		h.renderList(w, r, showHidden)
		return true
	}

	if path == "/atom.xml" {
		h.renderAtom(w, r)
		return true
	}

	if path == "/debug/gitsha" {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintln(w, gitSHA())
		return true
	}

	if strings.HasSuffix(path, "/") && path != "/" {
		trimmed := strings.TrimSuffix(path, "/")
		http.Redirect(w, r, trimmed, http.StatusMovedPermanently)
		return true
	}

	// Redirect old slug from filename typo.
	if path == "/jan06-update" {
		http.Redirect(w, r, "/jan26-update", http.StatusMovedPermanently)
		return true
	}

	if entry, ok := h.store.Entry(path); ok {
		if !showHidden && !entry.IsPublic(h.now()) {
			http.Redirect(w, r, "/__exe.dev/login?redirect="+path, http.StatusFound)
			return true
		}
		h.recordPageHit(entry.Path)
		h.renderEntry(w, r, entry, showHidden)
		return true
	}

	if asset, ok := h.store.Asset(path); ok {
		http.ServeContent(w, r, path, asset.ModTime, bytes.NewReader(asset.Data))
		return true
	}

	return false
}

func (h *Handler) renderEntry(w http.ResponseWriter, r *http.Request, entry *Entry, showHidden bool) {
	buf := new(bytes.Buffer)
	data := map[string]any{
		"Entry":      entry,
		"Entries":    h.entries(showHidden),
		"ShowHidden": showHidden,
		"ActivePage": "blog",
		"Now":        h.now(),
	}

	if err := h.templates.ExecuteTemplate(buf, "blog-entry.html", data); err != nil {
		http.Error(w, "error rendering blog entry", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(buf.Bytes())
}

func (h *Handler) renderList(w http.ResponseWriter, r *http.Request, showHidden bool) {
	buf := new(bytes.Buffer)
	data := map[string]any{
		"Entries":    h.entries(showHidden),
		"ShowHidden": showHidden,
		"ActivePage": "blog",
		"Now":        h.now(),
	}

	if err := h.templates.ExecuteTemplate(buf, "blog-list.html", data); err != nil {
		http.Error(w, "error rendering blog list", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(buf.Bytes())
}

func (h *Handler) renderAtom(w http.ResponseWriter, r *http.Request) {
	buf := new(bytes.Buffer)
	data := map[string]any{
		"Entries": h.entries(false),
		"Updated": h.store.Updated().Format("2006-01-02"),
	}

	if err := h.atomTemplate.Execute(buf, data); err != nil {
		http.Error(w, "error rendering atom feed", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/atom+xml; charset=utf-8")
	_, _ = w.Write(buf.Bytes())
}

func (h *Handler) recordPageHit(path string) {
	if h == nil || h.metrics == nil {
		return
	}
	h.metrics.RecordPageHit(path)
}

func parseMarkdownPost(relPath string, data []byte) (Entry, error) {
	base := filepath.Base(relPath)
	if !strings.HasSuffix(base, ".md") {
		return Entry{}, fmt.Errorf("markdown post missing .md extension: %s", relPath)
	}
	name := strings.TrimSuffix(base, ".md")
	if len(name) < len("2006-01-02-a") {
		return Entry{}, fmt.Errorf("blog filename too short: %s", relPath)
	}
	if name[4] != '-' || name[7] != '-' || name[10] != '-' {
		return Entry{}, fmt.Errorf("blog filename %s must follow YYYY-MM-DD-slug.md", relPath)
	}
	datePart := name[:10]
	slug := name[11:]
	if slug == "" {
		return Entry{}, fmt.Errorf("blog filename %s missing slug after date", relPath)
	}

	date, err := time.Parse("2006-01-02", datePart)
	if err != nil {
		return Entry{}, fmt.Errorf("parsing date in filename %s: %w", relPath, err)
	}

	ctx := parser.NewContext()
	var buf bytes.Buffer
	if err := markdown.Convert(data, &buf, parser.WithContext(ctx)); err != nil {
		return Entry{}, fmt.Errorf("rendering markdown: %w", err)
	}

	metadata := gmmeta.Get(ctx)
	if metadata == nil {
		return Entry{}, fmt.Errorf("missing front matter metadata in %s", relPath)
	}

	entry, err := entryFromMetadata("/"+slug, metadata, date)
	if err != nil {
		return Entry{}, err
	}
	entry.Slug = slug
	entry.Markdown = string(stripFrontMatter(data))
	entry.Content = template.HTML(buf.String())
	return entry, nil
}

func stripFrontMatter(data []byte) []byte {
	const start = "---\n"
	rest, ok := bytes.CutPrefix(data, []byte(start))
	if !ok {
		return data
	}
	const end = "\n---\n"
	_, after, ok := bytes.Cut(rest, []byte(end))
	if !ok {
		return data
	}
	return after
}

func entryFromMetadata(path string, metadata map[string]any, fileDate time.Time) (Entry, error) {
	entry := Entry{
		Path:      path,
		Published: true,
		Tags:      make([]string, 0),
	}

	title, err := metadataString(metadata, "title", true)
	if err != nil {
		return Entry{}, err
	}
	entry.Title = title

	description, err := metadataString(metadata, "description", true)
	if err != nil {
		return Entry{}, err
	}
	entry.Description = description

	author, err := metadataString(metadata, "author", true)
	if err != nil {
		return Entry{}, err
	}
	entry.Author = author

	dateValue, err := metadataString(metadata, "date", true)
	if err != nil {
		return Entry{}, err
	}
	metaDate, err := time.Parse("2006-01-02", dateValue)
	if err != nil {
		return Entry{}, fmt.Errorf("parsing front matter date %q: %w", dateValue, err)
	}
	if !metaDate.Equal(fileDate) {
		return Entry{}, fmt.Errorf("front matter date %s does not match filename date %s", dateValue, fileDate.Format("2006-01-02"))
	}
	entry.Date = metaDate
	entry.DateString = metaDate.Format("2006-01-02")
	entry.DateRFC3339 = metaDate.UTC().Format(time.RFC3339)

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

	if _, ok := metadata["embargo"]; ok {
		embargoStr, err := metadataString(metadata, "embargo", false)
		if err != nil {
			return Entry{}, err
		}
		if embargoStr != "" {
			embargo, err := time.Parse(time.RFC3339, embargoStr)
			if err != nil {
				return Entry{}, fmt.Errorf("parsing front matter embargo %q: %w", embargoStr, err)
			}
			entry.Embargo = embargo
		}
	}

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
	switch v := raw.(type) {
	case string:
		value := strings.TrimSpace(v)
		if value == "" && required {
			return "", fmt.Errorf("front matter field %s must not be empty", key)
		}
		return value, nil
	default:
		return "", fmt.Errorf("front matter field %s must be a string", key)
	}
}

func metadataBool(raw any, key string) (bool, error) {
	switch v := raw.(type) {
	case bool:
		return v, nil
	case string:
		value := strings.TrimSpace(strings.ToLower(v))
		switch value {
		case "true", "yes", "on", "1":
			return true, nil
		case "false", "no", "off", "0":
			return false, nil
		default:
			return false, fmt.Errorf("front matter field %s string value must be a boolean", key)
		}
	case int:
		return v != 0, nil
	case int64:
		return v != 0, nil
	case float64:
		return v != 0, nil
	default:
		return false, fmt.Errorf("front matter field %s must be a boolean", key)
	}
}

func gitSHA() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	var revision, dirty string
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
		case "vcs.modified":
			dirty = s.Value
		}
	}
	if revision == "" {
		return "unknown"
	}
	if len(revision) > 12 {
		revision = revision[:12]
	}
	if dirty == "true" {
		revision += "-dirty"
	}
	return revision
}

func metadataTags(raw any) ([]string, error) {
	switch v := raw.(type) {
	case []any:
		tags := make([]string, 0, len(v))
		for _, item := range v {
			switch tagVal := item.(type) {
			case string:
				tag := strings.TrimSpace(tagVal)
				if tag == "" {
					continue
				}
				tags = append(tags, tag)
			default:
				return nil, fmt.Errorf("tags entries must be strings, got %T", item)
			}
		}
		return tags, nil
	case []string:
		tags := make([]string, 0, len(v))
		for _, tag := range v {
			tag = strings.TrimSpace(tag)
			if tag != "" {
				tags = append(tags, tag)
			}
		}
		return tags, nil
	case string:
		parts := strings.Split(v, ",")
		tags := make([]string, 0, len(parts))
		for _, part := range parts {
			tag := strings.TrimSpace(part)
			if tag != "" {
				tags = append(tags, tag)
			}
		}
		return tags, nil
	default:
		return nil, fmt.Errorf("tags must be string, array of strings, or list, got %T", raw)
	}
}
