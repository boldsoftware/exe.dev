package security

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
	"sort"
	"strings"
	"time"

	"exe.dev/blog"
	templatespkg "exe.dev/templates"
	"github.com/yuin/goldmark"
	gmmeta "github.com/yuin/goldmark-meta"
	"github.com/yuin/goldmark/parser"
	htmlrenderer "github.com/yuin/goldmark/renderer/html"
)

var loadTime = time.Now()

//go:embed *.md
var bulletinsFS embed.FS

//go:embed bulletin-entry.html bulletin-list.html atom.xml topbar.html
var templateFS embed.FS

var markdown = goldmark.New(
	goldmark.WithExtensions(gmmeta.Meta),
	goldmark.WithRendererOptions(htmlrenderer.WithUnsafe()),
	goldmark.WithRenderer(blog.Renderer()),
)

var (
	defaultTemplates = template.Must(parseTemplates(templateFS))
	defaultAtomTmpl  = template.Must(parseAtomTemplate(templateFS))
)

func parseTemplates(fsys fs.FS) (*template.Template, error) {
	tmpl, err := template.New("security").ParseFS(fsys, "bulletin-entry.html", "bulletin-list.html")
	if err != nil {
		return nil, err
	}
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

// Bulletin is a security bulletin entry.
type Bulletin struct {
	Path        string
	Slug        string
	Markdown    string
	Author      string
	Title       string
	Description string
	Severity    string // e.g. "low", "medium", "high", "critical", "informational"
	Published   bool
	Date        time.Time
	DateString  string
	DateRFC3339 string
	Content     template.HTML
}

// Store holds parsed security bulletins.
type Store struct {
	entries     []*Bulletin
	byPath      map[string]*Bulletin
	bySlug      map[string]*Bulletin
	defaultPath string
	updated     time.Time
}

// Load reads bulletins from embedded files.
func Load(includeUnpublished bool) (*Store, error) {
	return loadStoreFromFS(bulletinsFS, includeUnpublished)
}

// LoadFromDir reads bulletins from a directory on disk.
func LoadFromDir(dir string, includeUnpublished bool) (*Store, error) {
	cleaned := filepath.Clean(dir)
	if cleaned == "" {
		cleaned = "."
	}
	return loadStoreFromFS(os.DirFS(cleaned), includeUnpublished)
}

func loadStoreFromFS(fsys fs.FS, includeUnpublished bool) (*Store, error) {
	store := &Store{
		byPath: make(map[string]*Bulletin),
		bySlug: make(map[string]*Bulletin),
	}

	if _, err := fs.ReadDir(fsys, "."); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return store, nil
		}
		return nil, fmt.Errorf("reading security bulletins directory: %w", err)
	}

	err := fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == "." || d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(path), ".md") {
			return nil
		}
		base := filepath.Base(path)
		if len(base) < len("2006-01-02-a.md") || base[4] != '-' || base[7] != '-' || base[10] != '-' {
			return nil // skip non-bulletin files like AGENTS.md
		}

		data, err := fs.ReadFile(fsys, path)
		if err != nil {
			return fmt.Errorf("reading %s: %w", path, err)
		}

		bulletin, err := parseBulletin(path, data)
		if err != nil {
			return fmt.Errorf("parsing %s: %w", path, err)
		}
		if bulletin.Published || includeUnpublished {
			copy := bulletin
			store.entries = append(store.entries, &copy)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	sort.Slice(store.entries, func(i, j int) bool {
		a, b := store.entries[i], store.entries[j]
		if !a.Date.Equal(b.Date) {
			return a.Date.After(b.Date)
		}
		return a.Title < b.Title
	})

	for _, b := range store.entries {
		store.byPath[b.Path] = b
		store.bySlug[b.Slug] = b
		if store.updated.IsZero() || b.Date.After(store.updated) {
			store.updated = b.Date
		}
	}

	if len(store.entries) > 0 {
		store.defaultPath = store.entries[0].Path
	}

	return store, nil
}

func (s *Store) Entries() []*Bulletin { return s.entries }
func (s *Store) Updated() time.Time   { return s.updated }

func (s *Store) Entry(path string) (*Bulletin, bool) {
	b, ok := s.byPath[path]
	return b, ok
}

// Handler serves security bulletin pages.
type Handler struct {
	store        *Store
	showHidden   bool
	templates    *template.Template
	atomTemplate *template.Template
}

// NewHandler creates a security bulletin handler with embedded templates.
func NewHandler(store *Store, showHidden bool) *Handler {
	return NewHandlerWithTemplates(store, showHidden, defaultTemplates, defaultAtomTmpl)
}

// NewHandlerWithTemplates creates a handler with provided templates.
func NewHandlerWithTemplates(store *Store, showHidden bool, templates, atom *template.Template) *Handler {
	if store == nil || templates == nil || atom == nil {
		return nil
	}
	return &Handler{
		store:        store,
		showHidden:   showHidden,
		templates:    templates,
		atomTemplate: atom,
	}
}

func (h *Handler) shouldShowHidden(r *http.Request) bool {
	if h == nil {
		return false
	}
	if h.showHidden {
		return true
	}
	return blog.CanPreviewUnpublished(r)
}

func (h *Handler) entries(showHidden bool) []*Bulletin {
	if h == nil || h.store == nil {
		return nil
	}
	all := h.store.Entries()
	if showHidden {
		return all
	}
	filtered := make([]*Bulletin, 0, len(all))
	for _, b := range all {
		if b.Published {
			filtered = append(filtered, b)
		}
	}
	return filtered
}

// Handle serves security bulletin requests under /security.
// It returns true if it handled the request.
func (h *Handler) Handle(w http.ResponseWriter, r *http.Request) bool {
	if h == nil || h.store == nil {
		return false
	}

	showHidden := h.shouldShowHidden(r)

	// Strip /security prefix to get the sub-path.
	path := strings.TrimPrefix(r.URL.Path, "/security")
	if path == "" {
		path = "/"
	}

	if path == "/" {
		h.renderList(w, r, showHidden)
		return true
	}

	if path == "/atom.xml" {
		h.renderAtom(w, r)
		return true
	}

	if strings.HasSuffix(path, "/") && path != "/" {
		trimmed := "/security" + strings.TrimSuffix(path, "/")
		http.Redirect(w, r, trimmed, http.StatusMovedPermanently)
		return true
	}

	if bulletin, ok := h.store.Entry(path); ok {
		if !showHidden && !bulletin.Published {
			http.Redirect(w, r, "/__exe.dev/login?redirect=/security"+path, http.StatusFound)
			return true
		}
		h.renderEntry(w, r, bulletin, showHidden)
		return true
	}

	return false
}

func (h *Handler) renderEntry(w http.ResponseWriter, _ *http.Request, bulletin *Bulletin, showHidden bool) {
	buf := new(bytes.Buffer)
	data := map[string]any{
		"Bulletin":   bulletin,
		"Entries":    h.entries(showHidden),
		"ShowHidden": showHidden,
		"ActivePage": "security",
	}
	if err := h.templates.ExecuteTemplate(buf, "bulletin-entry.html", data); err != nil {
		http.Error(w, "error rendering security bulletin", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(buf.Bytes())
}

func (h *Handler) renderList(w http.ResponseWriter, _ *http.Request, showHidden bool) {
	buf := new(bytes.Buffer)
	data := map[string]any{
		"Entries":    h.entries(showHidden),
		"ShowHidden": showHidden,
		"ActivePage": "security",
	}
	if err := h.templates.ExecuteTemplate(buf, "bulletin-list.html", data); err != nil {
		http.Error(w, "error rendering security bulletins", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(buf.Bytes())
}

func (h *Handler) renderAtom(w http.ResponseWriter, _ *http.Request) {
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

func parseBulletin(relPath string, data []byte) (Bulletin, error) {
	base := filepath.Base(relPath)
	if !strings.HasSuffix(base, ".md") {
		return Bulletin{}, fmt.Errorf("bulletin missing .md extension: %s", relPath)
	}
	name := strings.TrimSuffix(base, ".md")
	if len(name) < len("2006-01-02-a") {
		return Bulletin{}, fmt.Errorf("bulletin filename too short: %s", relPath)
	}
	if name[4] != '-' || name[7] != '-' || name[10] != '-' {
		return Bulletin{}, fmt.Errorf("bulletin filename %s must follow YYYY-MM-DD-slug.md", relPath)
	}
	datePart := name[:10]
	slug := name[11:]
	if slug == "" {
		return Bulletin{}, fmt.Errorf("bulletin filename %s missing slug after date", relPath)
	}

	date, err := time.Parse("2006-01-02", datePart)
	if err != nil {
		return Bulletin{}, fmt.Errorf("parsing date in filename %s: %w", relPath, err)
	}

	ctx := parser.NewContext()
	var buf bytes.Buffer
	if err := markdown.Convert(data, &buf, parser.WithContext(ctx)); err != nil {
		return Bulletin{}, fmt.Errorf("rendering markdown: %w", err)
	}

	metadata := gmmeta.Get(ctx)
	if metadata == nil {
		return Bulletin{}, fmt.Errorf("missing front matter metadata in %s", relPath)
	}

	b := Bulletin{
		Path:      "/" + slug,
		Slug:      slug,
		Published: true,
	}

	b.Title, err = metadataString(metadata, "title", true)
	if err != nil {
		return Bulletin{}, err
	}

	b.Description, err = metadataString(metadata, "description", true)
	if err != nil {
		return Bulletin{}, err
	}

	b.Author, err = metadataString(metadata, "author", true)
	if err != nil {
		return Bulletin{}, err
	}

	b.Severity, err = metadataString(metadata, "severity", false)
	if err != nil {
		return Bulletin{}, err
	}

	dateValue, err := metadataString(metadata, "date", true)
	if err != nil {
		return Bulletin{}, err
	}
	metaDate, err := time.Parse("2006-01-02", dateValue)
	if err != nil {
		return Bulletin{}, fmt.Errorf("parsing front matter date %q: %w", dateValue, err)
	}
	if !metaDate.Equal(date) {
		return Bulletin{}, fmt.Errorf("front matter date %s does not match filename date %s", dateValue, date.Format("2006-01-02"))
	}
	b.Date = metaDate
	b.DateString = metaDate.Format("2006-01-02")
	b.DateRFC3339 = metaDate.UTC().Format(time.RFC3339)

	if raw, ok := metadata["published"]; ok {
		b.Published, err = metadataBool(raw, "published")
		if err != nil {
			return Bulletin{}, err
		}
	}

	b.Markdown = string(stripFrontMatter(data))
	b.Content = template.HTML(buf.String())
	return b, nil
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

func metadataString(metadata map[string]any, key string, required bool) (string, error) {
	raw, ok := metadata[key]
	if !ok || raw == nil {
		if required {
			return "", fmt.Errorf("missing required front matter field: %s", key)
		}
		return "", nil
	}
	v, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("front matter field %s must be a string", key)
	}
	value := strings.TrimSpace(v)
	if value == "" && required {
		return "", fmt.Errorf("front matter field %s must not be empty", key)
	}
	return value, nil
}

func metadataBool(raw any, key string) (bool, error) {
	switch v := raw.(type) {
	case bool:
		return v, nil
	case string:
		switch strings.TrimSpace(strings.ToLower(v)) {
		case "true", "yes", "1":
			return true, nil
		case "false", "no", "0":
			return false, nil
		default:
			return false, fmt.Errorf("front matter field %s must be a boolean", key)
		}
	default:
		return false, fmt.Errorf("front matter field %s must be a boolean", key)
	}
}
