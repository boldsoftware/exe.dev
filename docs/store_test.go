package docs

import (
	"testing"
)

func TestParseMarkdownDocStripsFrontMatter(t *testing.T) {
	const markdown = `---
title: Example Doc
description: short desc
---
Hello **world**!
`

	entry, err := parseMarkdownDoc("example-doc.md", []byte(markdown))
	if err != nil {
		t.Fatalf("parseMarkdownDoc returned error: %v", err)
	}

	if entry.Markdown != "Hello **world**!\n" {
		t.Fatalf("unexpected markdown body: %q", entry.Markdown)
	}
}
