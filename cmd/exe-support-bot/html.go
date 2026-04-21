package main

import (
	"html"
	"strings"
)

// htmlToText does a best-effort plain-text extraction. Not a full HTML parser;
// support messages come in many shapes and we just want something searchable.
func htmlToText(s string) string {
	if s == "" {
		return ""
	}
	if !strings.ContainsRune(s, '<') {
		return html.UnescapeString(strings.TrimSpace(s))
	}
	var b strings.Builder
	b.Grow(len(s))
	inTag := false
	inScript := false
	inStyle := false
	lower := strings.ToLower(s)
	i := 0
	for i < len(s) {
		if !inTag {
			if !inScript && !inStyle && s[i] != '<' {
				b.WriteByte(s[i])
				i++
				continue
			}
			if s[i] == '<' {
				// detect <br>, <p>, <div> → newline
				rest := lower[i:]
				switch {
				case strings.HasPrefix(rest, "<br"), strings.HasPrefix(rest, "</p"), strings.HasPrefix(rest, "</div"), strings.HasPrefix(rest, "</li"), strings.HasPrefix(rest, "</tr"):
					b.WriteByte('\n')
				case strings.HasPrefix(rest, "<script"):
					inScript = true
				case strings.HasPrefix(rest, "<style"):
					inStyle = true
				case strings.HasPrefix(rest, "</script"):
					inScript = false
				case strings.HasPrefix(rest, "</style"):
					inStyle = false
				}
				inTag = true
				i++
				continue
			}
			i++
			continue
		}
		if s[i] == '>' {
			inTag = false
		}
		i++
	}
	out := html.UnescapeString(b.String())
	// Collapse whitespace runs.
	var c strings.Builder
	c.Grow(len(out))
	var lastWasSpace, lastWasNL bool
	for _, r := range out {
		switch r {
		case ' ', '\t':
			if !lastWasSpace && !lastWasNL {
				c.WriteByte(' ')
			}
			lastWasSpace = true
		case '\n':
			if !lastWasNL {
				c.WriteByte('\n')
			}
			lastWasNL = true
			lastWasSpace = false
		case '\r':
			// skip
		default:
			c.WriteRune(r)
			lastWasSpace = false
			lastWasNL = false
		}
	}
	return strings.TrimSpace(c.String())
}
