package debug_templates

import (
	"embed"
	"fmt"
	"html/template"
	"net/url"
	"strings"
	"sync"
	"time"

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
			"maskToken": func(s string) string {
				if len(s) <= 8 {
					return "***"
				}
				return s[:4] + "..." + s[len(s)-4:]
			},
			"tokenStatus": func(s *string) template.HTML {
				if s == nil {
					return `<span class="status-unknown">-</span>`
				}
				var t time.Time
				var err error
				for _, fmt := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} {
					t, err = time.Parse(fmt, *s)
					if err == nil {
						break
					}
				}
				if err != nil {
					return template.HTML(fmt.Sprintf(`<span class="status-unknown">%s</span>`, template.HTMLEscapeString(*s)))
				}
				remaining := time.Until(t)
				ts := t.Format("2006-01-02 15:04")
				if remaining <= 0 {
					return template.HTML(fmt.Sprintf(`<span class="status-expired">expired</span> <span title="%s">%s ago</span>`, ts, remaining.Round(time.Minute).Abs()))
				}
				return template.HTML(fmt.Sprintf(`<span class="status-ok">valid</span> <span title="%s">%s left</span>`, ts, remaining.Round(time.Minute)))
			},
			"fmtTime": func(t *time.Time) string {
				if t == nil {
					return "-"
				}
				return t.UTC().Format("2006-01-02 15:04:05")
			},
			"hasPrefix":     strings.HasPrefix,
			"urlPathEscape": url.PathEscape,
		}
		debugTemplate, debugTemplateErr = template.New("").Funcs(funcs).ParseFS(Files, "*.html")
	})
	return debugTemplate, debugTemplateErr
}
