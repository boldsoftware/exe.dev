package templates

import (
	"embed"
	"fmt"
	"html/template"
	"strings"
)

//go:embed *.html
var Files embed.FS

// Parse parses all HTML templates with the standard function map.
func Parse() (*template.Template, error) {
	funcMap := template.FuncMap{
		"contains": strings.Contains,
	}
	tmpl, err := template.New("").Funcs(funcMap).ParseFS(Files, "*.html")
	if err != nil {
		return nil, fmt.Errorf("failed to parse templates: %w", err)
	}
	return tmpl, nil
}
