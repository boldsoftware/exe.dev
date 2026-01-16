package debug_templates

import (
	"embed"
	"html/template"
)

//go:embed *.html
var Files embed.FS

// Parse parses all HTML templates with common functions.
func Parse() (*template.Template, error) {
	return template.New("").Funcs(template.FuncMap{
		"safeHTML": func(s string) template.HTML {
			return template.HTML(s)
		},
	}).ParseFS(Files, "*.html")
}
