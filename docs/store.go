package docs

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
	"strconv"
	"strings"
	"time"

	"exe.dev/stage"
	templatespkg "exe.dev/templates"
	"github.com/yuin/goldmark"
	gmmeta "github.com/yuin/goldmark-meta"
	"github.com/yuin/goldmark/parser"
	htmlrenderer "github.com/yuin/goldmark/renderer/html"
)

var loadTime = time.Now()

//go:embed content
var contentFS embed.FS

//go:embed doc-entry.html doc-section-content.html docs-list.html
var templateFS embed.FS

var docTemplates = template.Must(func() (*template.Template, error) {
	tmpl, err := template.New("docs").ParseFS(templateFS, "doc-entry.html", "doc-section-content.html", "docs-list.html")
	if err != nil {
		return nil, err
	}
	if _, err := tmpl.ParseFS(templatespkg.Files, "topbar.html"); err != nil {
		return nil, err
	}
	return tmpl, nil
}())

var markdown = goldmark.New(
	goldmark.WithExtensions(gmmeta.Meta),
	goldmark.WithParserOptions(parser.WithAutoHeadingID()),
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
	Preview     bool
	Unlinked    bool
	Content     template.HTML
}

// Visible reports whether the entry should be shown to users.
// An entry is visible if it is published or in preview.
func (e *Entry) Visible() bool {
	return e.Published || e.Preview
}

type Group struct {
	Heading string
	Slug    string
	Docs    []*Entry
}

type Store struct {
	entries     []*Entry
	groups      []Group
	byPath      map[string]*Entry
	bySlug      map[string]*Entry
	byGroupSlug map[string]*Group
	assets      map[string]Asset
	defaultPath string
}

func Load(env stage.Env) (*Store, error) {
	return loadFromFS(contentFS, env)
}

// LoadFromDir loads docs content from a directory on disk.
// The directory should contain a "content" subdirectory with markdown files.
func LoadFromDir(dir string, env stage.Env) (*Store, error) {
	return loadFromFS(os.DirFS(dir), env)
}

// ParseTemplatesFromDir parses doc templates from disk.
// docsDir should contain doc-entry.html and docs-list.html.
// topbarPath is the path to the topbar.html template file.
func ParseTemplatesFromDir(docsDir, topbarPath string) (*template.Template, error) {
	tmpl, err := template.New("docs").ParseFiles(
		filepath.Join(docsDir, "doc-entry.html"),
		filepath.Join(docsDir, "doc-section-content.html"),
		filepath.Join(docsDir, "docs-list.html"),
	)
	if err != nil {
		return nil, err
	}
	if _, err := tmpl.ParseFiles(topbarPath); err != nil {
		return nil, err
	}
	return tmpl, nil
}

func loadFromFS(fsys fs.FS, env stage.Env) (*Store, error) {
	store := &Store{
		byPath:      make(map[string]*Entry),
		bySlug:      make(map[string]*Entry),
		byGroupSlug: make(map[string]*Group),
		assets:      make(map[string]Asset),
	}

	if _, err := fs.ReadDir(fsys, "content"); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return store, nil
		}
		return nil, fmt.Errorf("reading docs content directory: %w", err)
	}

	err := fs.WalkDir(fsys, "content", func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		if d.IsDir() {
			return nil
		}

		data, err := fs.ReadFile(fsys, path)
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
			if entry.Published || (entry.Preview && env.ShowDocsPreview) || env.ShowHiddenDocs {
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
		if a.Visible() != b.Visible() {
			return a.Visible()
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
	for i := range store.groups {
		store.byGroupSlug[store.groups[i].Slug] = &store.groups[i]
	}

	for _, entry := range store.entries {
		store.byPath[entry.Path] = entry
		if entry.Slug != "" {
			store.bySlug[entry.Slug] = entry
		}
	}

	for _, entry := range store.entries {
		if entry.Published && !entry.Unlinked {
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

// Entries returns all published, linked entries for use in sitemaps and similar.
func (s *Store) Entries() []*Entry {
	var result []*Entry
	for _, entry := range s.entries {
		if entry.Published && !entry.Unlinked {
			result = append(result, entry)
		}
	}
	return result
}

func (s *Store) Entry(path string) (*Entry, bool) {
	entry, ok := s.byPath[path]
	return entry, ok
}

func (s *Store) Asset(path string) (Asset, bool) {
	asset, ok := s.assets[path]
	return asset, ok
}

func (s *Store) GroupBySlug(slug string) (*Group, bool) {
	if s == nil {
		return nil, false
	}
	group, ok := s.byGroupSlug[slug]
	return group, ok
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
	tmpl       *template.Template
}

func NewHandler(store *Store, showHidden bool) *Handler {
	if store == nil {
		return nil
	}
	return &Handler{store: store, showHidden: showHidden, tmpl: docTemplates}
}

// NewHandlerWithTemplates creates a Handler that uses the given templates
// instead of the package-level embedded templates.
func NewHandlerWithTemplates(store *Store, showHidden bool, tmpl *template.Template) *Handler {
	if store == nil {
		return nil
	}
	return &Handler{store: store, showHidden: showHidden, tmpl: tmpl}
}

// statusTag returns a label for the entry's visibility state, for use in templates.
// Preview entries get " [preview]", hidden drafts get " [draft]", everything else "".
func (h *Handler) statusTag(e *Entry) string {
	if e.Preview {
		return " [preview]"
	}
	if h.showHidden && !e.Visible() {
		return " [draft]"
	}
	return ""
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
		// Redirect to the first doc page
		if h.store.defaultPath != "" {
			http.Redirect(w, r, h.store.defaultPath, http.StatusTemporaryRedirect)
			return true
		}
		// Fallback to TOC if no default path
		h.renderDocsList(w, r)
		return true
	}

	if path == "/docs/list" {
		h.renderDocsList(w, r)
		return true
	}

	if path == "/docs.md" {
		h.renderDocsIndex(w, r)
		return true
	}

	if path == "/llms.txt" || path == "/llms-full.txt" {
		h.renderAllDocsMD(w, r)
		return true
	}

	if path == "/docs/all.md" {
		h.renderAllDocsMD(w, r)
		return true
	}

	if path == "/docs/all" {
		h.renderAllDocsHTML(w, r)
		return true
	}

	if sectionSlug, ok := strings.CutPrefix(path, "/docs/section/"); ok && sectionSlug != "" {
		group, found := h.store.GroupBySlug(sectionSlug)
		if !found {
			return false
		}
		h.renderDocSection(w, r, group)
		return true
	}

	// Handle /docs/{slug}.md -> serve raw markdown
	if basePath, ok := strings.CutSuffix(path, ".md"); ok {
		if entry, ok := h.store.Entry(basePath); ok {
			h.renderDocMarkdown(w, r, entry)
			return true
		}
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

	if asset, ok := h.store.Asset(path); ok {
		http.ServeContent(w, r, path, asset.ModTime, bytes.NewReader(asset.Data))
		return true
	}

	return false
}

func (h *Handler) renderDocEntry(w http.ResponseWriter, r *http.Request, entry *Entry) {
	buf := new(bytes.Buffer)
	data := map[string]any{
		"Entry":     entry,
		"Groups":    h.store.Groups(),
		"StatusTag": h.statusTag,
	}

	if err := h.tmpl.ExecuteTemplate(buf, "doc-entry.html", data); err != nil {
		http.Error(w, "error rendering doc", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(buf.Bytes())
}

func (h *Handler) renderDocSection(w http.ResponseWriter, _ *http.Request, group *Group) {
	type visibleEntry struct {
		Path, Title, Description string
	}
	var docs []visibleEntry
	for _, entry := range group.Docs {
		if !entry.Visible() && !h.showHidden {
			continue
		}
		docs = append(docs, visibleEntry{
			Path:        entry.Path,
			Title:       entry.Title + h.statusTag(entry),
			Description: entry.Description,
		})
	}
	var contentBuf bytes.Buffer
	if err := h.tmpl.ExecuteTemplate(&contentBuf, "doc-section-content.html", docs); err != nil {
		http.Error(w, "error rendering doc section", http.StatusInternalServerError)
		return
	}

	sectionEntry := &Entry{
		Path:        "/docs/section/" + group.Slug,
		Slug:        "section/" + group.Slug,
		Title:       group.Heading,
		Description: "exe.dev documentation: " + group.Heading,
		Content:     template.HTML(contentBuf.String()),
	}

	buf := new(bytes.Buffer)
	data := map[string]any{
		"Entry":     sectionEntry,
		"Groups":    h.store.Groups(),
		"StatusTag": h.statusTag,
	}

	if err := h.tmpl.ExecuteTemplate(buf, "doc-entry.html", data); err != nil {
		http.Error(w, "error rendering doc section", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(buf.Bytes())
}

func (h *Handler) renderDocsList(w http.ResponseWriter, r *http.Request) {
	buf := new(bytes.Buffer)
	data := map[string]any{
		"Groups":    h.store.Groups(),
		"StatusTag": h.statusTag,
	}

	if err := h.tmpl.ExecuteTemplate(buf, "docs-list.html", data); err != nil {
		http.Error(w, "error rendering docs", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(buf.Bytes())
}

func (h *Handler) renderDocMarkdown(w http.ResponseWriter, _ *http.Request, entry *Entry) {
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	_, _ = w.Write([]byte(entry.Markdown))
}

func (h *Handler) renderDocsIndex(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	fmt.Fprintf(w, "# exe.dev docs\n\n")

	for _, group := range h.store.Groups() {
		if group.Heading != "" {
			fmt.Fprintf(w, "## %s\n\n", group.Heading)
		}
		for _, entry := range group.Docs {
			if !entry.Visible() && !h.showHidden {
				continue
			}
			fmt.Fprintf(w, "- [%s](/docs/%s.md)", entry.Title, entry.Slug)
			if entry.Description != "" {
				fmt.Fprintf(w, " - %s", entry.Description)
			}
			fmt.Fprintln(w)
		}
		fmt.Fprintln(w)
	}
}

func groupDocsByHeading(entries []*Entry) []Group {
	groupMap := make(map[string][]*Entry)
	var headingOrder []string
	headingsSeen := make(map[string]bool)

	for _, entry := range entries {
		if entry.Unlinked {
			continue
		}
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
			Slug:    groupSlug(heading),
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

func groupSlug(heading string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			return r
		case r == ' ':
			return '-'
		default:
			return -1
		}
	}, heading)
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
	// Store markdown without front matter for terminal rendering.
	entry.Markdown = string(stripFrontMatter(data))
	entry.Content = template.HTML(buf.String())
	return entry, nil
}

// stripFrontMatter removes a leading YAML front matter block delimited by lines containing only "---".
// If no such block exists at the file start, the original content is returned unchanged.
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
	// Skip past the closing delimiter and return the remainder.
	return after
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

	if raw, ok := metadata["preview"]; ok {
		preview, err := metadataBool(raw, "preview")
		if err != nil {
			return Entry{}, err
		}
		entry.Preview = preview
		if preview {
			entry.Published = false
		}
	}

	if raw, ok := metadata["unlinked"]; ok {
		unlinked, err := metadataBool(raw, "unlinked")
		if err != nil {
			return Entry{}, err
		}
		entry.Unlinked = unlinked
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
	for tag := range strings.SplitSeq(content, ",") {
		tag = strings.TrimSpace(tag)
		if tag != "" {
			tags = append(tags, tag)
		}
	}
	return tags
}

func (h *Handler) renderAllDocsMD(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")

	firstDoc := true
	for _, group := range h.store.Groups() {
		for _, entry := range group.Docs {
			if !entry.Visible() && !h.showHidden {
				continue
			}

			if !firstDoc {
				fmt.Fprintf(w, "\n\n---\n\n")
			}
			firstDoc = false

			if entry.Title != "" {
				fmt.Fprintf(w, "# %s\n\n", entry.Title)
			}
			if entry.Subheading != "" {
				fmt.Fprintf(w, "**%s**\n\n", entry.Subheading)
			}
			if entry.Description != "" {
				fmt.Fprintf(w, "*%s*\n\n", entry.Description)
			}
			fmt.Fprint(w, entry.Markdown)
		}
	}
}

func (h *Handler) renderAllDocsHTML(w http.ResponseWriter, r *http.Request) {
	// Create combined content with anchors (TOC is in the sidebar)
	var contentBuf bytes.Buffer
	firstGroup := true
	for _, group := range h.store.Groups() {
		if !firstGroup {
			contentBuf.WriteString("<hr style=\"margin: 48px 0;\">\n")
		}
		firstGroup = false

		contentBuf.WriteString("<h2>")
		contentBuf.WriteString(template.HTMLEscapeString(group.Heading))
		contentBuf.WriteString("</h2>\n")

		for _, entry := range group.Docs {
			if !entry.Visible() && !h.showHidden {
				continue
			}
			contentBuf.WriteString("<h3 id=\"")
			contentBuf.WriteString(entry.Slug)
			contentBuf.WriteString("\" class=\"anchor-heading\">")
			contentBuf.WriteString(template.HTMLEscapeString(entry.Title))
			contentBuf.WriteString("<a href=\"#")
			contentBuf.WriteString(entry.Slug)
			contentBuf.WriteString("\" class=\"anchor-link\" aria-label=\"Link to this section\">#</a>")
			contentBuf.WriteString("</h3>\n")
			contentBuf.WriteString(string(entry.Content))
			contentBuf.WriteString("\n")
		}
	}

	allEntry := &Entry{
		Path:        "/docs/all",
		Slug:        "all",
		Title:       "exe.dev Documentation",
		Description: "Complete exe.dev documentation in one page",
		Content:     template.HTML(contentBuf.String()),
	}

	buf := new(bytes.Buffer)
	data := map[string]any{
		"Entry":     allEntry,
		"Groups":    h.store.Groups(),
		"StatusTag": h.statusTag,
	}

	if err := h.tmpl.ExecuteTemplate(buf, "doc-entry.html", data); err != nil {
		http.Error(w, "error rendering doc", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(buf.Bytes())
}
