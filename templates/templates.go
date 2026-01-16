package templates

import (
	"embed"
	"fmt"
	"html/template"
	"strings"
	"time"

	"github.com/dustin/go-humanize"
)

//go:embed *.html
var Files embed.FS

// Parse parses all HTML templates with the standard function map.
func Parse() (*template.Template, error) {
	funcMap := template.FuncMap{
		"contains":      strings.Contains,
		"formatTimeAgo": formatTimeAgo,
	}
	tmpl, err := template.New("").Funcs(funcMap).ParseFS(Files, "*.html")
	if err != nil {
		return nil, fmt.Errorf("failed to parse templates: %w", err)
	}
	return tmpl, nil
}

// formatTimeAgo returns a human-readable relative time string
func formatTimeAgo(t *time.Time) string {
	if t == nil {
		return ""
	}
	return humanize.Time(*t)
}
