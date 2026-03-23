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
		"contains":           strings.Contains,
		"formatTimeAgo":      formatTimeAgo,
		"formatVagueTimeAgo": formatVagueTimeAgo,
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

// formatVagueTimeAgo returns a deliberately vague relative time string,
// avoiding precision that could be confusing across time zones.
func formatVagueTimeAgo(t *time.Time) string {
	if t == nil {
		return ""
	}
	days := int(time.Since(*t).Hours() / 24)
	switch {
	case days < 7:
		return "less than 1 week ago"
	case days < 14:
		return "1 week ago"
	case days < 21:
		return "2 weeks ago"
	case days < 30:
		return "3 weeks ago"
	case days < 60:
		return "1 month ago"
	default:
		months := days / 30
		if months < 12 {
			return fmt.Sprintf("%d months ago", months)
		}
		years := months / 12
		if years == 1 {
			return "1 year ago"
		}
		return fmt.Sprintf("%d years ago", years)
	}
}
