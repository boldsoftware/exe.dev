// Package idea defines the data and validation for idea templates
// shown on the /new page.
package idea

import (
	"math/rand/v2"
	"regexp"

	"exe.dev/exedb"
)

// Category defines a template category and its display label.
type Category struct {
	Slug  string
	Label string
}

// Categories defines the allowed categories and their display order.
var Categories = []Category{
	{"dev-tools", "Dev Tools"},
	{"web-apps", "Web Apps"},
	{"ai-ml", "AI & ML"},
	{"databases", "Databases"},
	{"games", "Games & Fun"},
	{"self-hosted", "Self-Hosted"},
	{"other", "Other"},
}

// ValidSlugRe matches a valid template slug.
var ValidSlugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,62}[a-z0-9]$`)

// suffixWords matches the JS list in new.js.
var suffixWords = []string{
	"alpha", "bravo", "delta", "echo", "fox", "gold", "hawk", "jade", "kilo",
	"lima", "nova", "oak", "pine", "rain", "sky", "star", "tide", "wolf", "zen",
}

// RandomName returns shortname + "-" + random suffix, e.g. "openclaw-wolf".
func RandomName(shortname string) string {
	return shortname + "-" + suffixWords[rand.IntN(len(suffixWords))]
}

// JSON is the API representation of a template.
type JSON struct {
	ID               int64   `json:"id"`
	Slug             string  `json:"slug"`
	Title            string  `json:"title"`
	ShortDescription string  `json:"short_description"`
	Category         string  `json:"category"`
	Prompt           string  `json:"prompt"`
	IconURL          string  `json:"icon_url"`
	ScreenshotURL    string  `json:"screenshot_url"`
	Featured         bool    `json:"featured"`
	AvgRating        float64 `json:"avg_rating"`
	RatingCount      int64   `json:"rating_count"`
	VMShortname      string  `json:"vm_shortname"`
	Image            string  `json:"image"`
	DeployCount      int64   `json:"deploy_count"`
}

// ApprovedRowToJSON converts a database row to its API representation.
func ApprovedRowToJSON(r exedb.ListApprovedTemplatesRow) JSON {
	return JSON{
		ID:               r.ID,
		Slug:             r.Slug,
		Title:            r.Title,
		ShortDescription: r.ShortDescription,
		Category:         r.Category,
		Prompt:           r.Prompt,
		IconURL:          r.IconURL,
		ScreenshotURL:    r.ScreenshotURL,
		Featured:         r.Featured,
		AvgRating:        r.AvgRating,
		RatingCount:      r.RatingCount,
		VMShortname:      r.VMShortname,
		Image:            r.Image,
		DeployCount:      r.DeployCount,
	}
}

// ValidCategory returns true if slug is a known category.
func ValidCategory(slug string) bool {
	for _, c := range Categories {
		if c.Slug == slug {
			return true
		}
	}
	return false
}

// AllRowToJSON converts a ListAllTemplatesRow (used by admin) to JSON.
func AllRowToJSON(t exedb.ListAllTemplatesRow) JSON {
	return JSON{
		ID:               t.ID,
		Slug:             t.Slug,
		Title:            t.Title,
		ShortDescription: t.ShortDescription,
		Category:         t.Category,
		Prompt:           t.Prompt,
		IconURL:          t.IconURL,
		ScreenshotURL:    t.ScreenshotURL,
		Featured:         t.Featured,
		AvgRating:        t.AvgRating,
		RatingCount:      t.RatingCount,
		VMShortname:      t.VMShortname,
		Image:            t.Image,
		DeployCount:      t.DeployCount,
	}
}
