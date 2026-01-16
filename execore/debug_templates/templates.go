package debug_templates

import (
	"embed"
	"html/template"
)

//go:embed *.html
var Files embed.FS

// Parse parses all HTML templates.
func Parse() (*template.Template, error) {
	return template.ParseFS(Files, "*.html")
}
