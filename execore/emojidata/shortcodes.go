// Package emojidata provides a lookup and completion table of GitHub-flavored
// emoji shortcodes (e.g. ":duck:" -> "🦆"). The underlying data is extracted
// from github.com/yuin/goldmark-emoji via the generate_shortcodes.sh script in
// this directory.
package emojidata

import (
	_ "embed"
	"sort"
	"strings"
	"sync"
)

//go:embed shortcodes.tsv
var rawTSV string

type Entry struct {
	ShortName string
	Glyph     string
	Name      string
}

var (
	loadOnce sync.Once
	entries  []Entry
	byShort  map[string]Entry
)

func load() {
	loadOnce.Do(func() {
		byShort = make(map[string]Entry)
		for _, line := range strings.Split(rawTSV, "\n") {
			if line == "" {
				continue
			}
			parts := strings.SplitN(line, "\t", 3)
			if len(parts) != 3 {
				continue
			}
			e := Entry{ShortName: parts[0], Glyph: parts[1], Name: parts[2]}
			entries = append(entries, e)
			byShort[e.ShortName] = e
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].ShortName < entries[j].ShortName })
	})
}

// Lookup returns the emoji glyph for a shortname (without colons), or "" if
// unknown.
func Lookup(shortname string) string {
	load()
	if e, ok := byShort[shortname]; ok {
		return e.Glyph
	}
	return ""
}

// Entries returns all known shortcode entries, sorted by shortname.
func Entries() []Entry {
	load()
	return entries
}

// Resolve returns the emoji glyph for a value that may either be a bare emoji
// (returned as-is) or a ":shortcode:" form. Returns ("", false) if the shortcode
// is not recognized.
func Resolve(value string) (string, bool) {
	if len(value) >= 2 && value[0] == ':' && value[len(value)-1] == ':' {
		name := value[1 : len(value)-1]
		if g := Lookup(name); g != "" {
			return g, true
		}
		return "", false
	}
	return value, true
}
