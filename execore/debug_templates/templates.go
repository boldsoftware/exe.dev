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
			"tokenStatus": func(t *time.Time) template.HTML {
				if t == nil {
					return `<span class="status-unknown">-</span>`
				}
				remaining := time.Until(*t)
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
			"add":           func(a, b int) int { return a + b },
			"subtract":      func(a, b int) int { return a - b },
			"formatBytes": func(bytes uint64) string {
				if bytes == 0 {
					return "-"
				}
				const unit = 1024
				if bytes < unit {
					return fmt.Sprintf("%d B", bytes)
				}
				div, exp := uint64(unit), 0
				for n := bytes / unit; n >= unit; n /= unit {
					div *= unit
					exp++
				}
				return fmt.Sprintf("%.1f %ciB", float64(bytes)/float64(div), "KMGTPE"[exp])
			},
			"divf64": func(a int64, b float64) float64 {
				return float64(a) / b
			},
			"add64": func(a, b int64) int64 {
				return a + b
			},
			"overage": func(bytes int64, includedGB float64) string {
				const gib = 1 << 30
				gb := float64(bytes) / gib
				o := max(gb-includedGB, 0)
				return fmt.Sprintf("%.3f", o)
			},
			"diskCost": func(diskBytes int64, includedGB float64) string {
				const gib = 1 << 30
				const price = 0.08
				gb := float64(diskBytes) / gib
				cost := max(gb-includedGB, 0) * price
				return fmt.Sprintf("$%.2f", cost)
			},
			"bwCost": func(bwBytes int64, includedGB float64) string {
				const gib = 1 << 30
				const price = 0.07
				gb := float64(bwBytes) / gib
				cost := max(gb-includedGB, 0) * price
				return fmt.Sprintf("$%.2f", cost)
			},
		}
		debugTemplate, debugTemplateErr = template.New("").Funcs(funcs).ParseFS(Files, "*.html")
	})
	return debugTemplate, debugTemplateErr
}
