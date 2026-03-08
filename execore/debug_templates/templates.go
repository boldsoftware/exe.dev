package debug_templates

import (
	"embed"
	"html/template"

	"exe.dev/stage"
)

//go:embed *.html
var Files embed.FS

// Parse parses all HTML templates with common functions.
func Parse(env stage.Env) (*template.Template, error) {
	return template.New("").Funcs(template.FuncMap{
		"safeHTML": func(s string) template.HTML {
			return template.HTML(s)
		},
		"stageBgColor": func() string { return env.DebugBgColor },
	}).ParseFS(Files, "*.html")
}
