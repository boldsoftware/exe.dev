package debug_templates

import (
	"embed"
	"html/template"
	"sync"

	"exe.dev/stage"
)

//go:embed *.html
var Files embed.FS

var (
	debugTemplateOnce sync.Once
	debugTemplate     *template.Template
	debugTemplateErr  error
)

// Parse parses all HTML templates with common functions.
func Parse(env stage.Env) (*template.Template, error) {
	// This is always called with the same stage.Env value,
	// so we use a sync.Once.
	debugTemplateOnce.Do(func() {
		funcs := template.FuncMap{
			"safeHTML": func(s string) template.HTML {
				return template.HTML(s)
			},
			"stageBgColor": func() string { return env.DebugBgColor },
		}
		debugTemplate, debugTemplateErr = template.New("").Funcs(funcs).ParseFS(Files, "*.html")
	})
	return debugTemplate, debugTemplateErr
}
