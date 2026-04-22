package boxname

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestEmojiForName(t *testing.T) {
	t.Parallel()
	// Every word in the name vocabulary should be either mapped or explicitly
	// unmapped; we just verify that well-known words work and that unknown
	// names return (no match).
	if e, ok := EmojiForName("alpha-bravo", nil); !ok || e == "" {
		t.Errorf("expected mapping for alpha-bravo, got %q ok=%v", e, ok)
	}
	if _, ok := EmojiForName("xxyyzz-qqqqq", nil); ok {
		t.Errorf("expected no mapping for unknown words")
	}
}

func TestEmojiForNameRespectsAvoid(t *testing.T) {
	t.Parallel()
	// alpha -> 🅰️. If we avoid it with no other word, we should get no match.
	if _, ok := EmojiForName("alpha", map[string]bool{"🅰️": true}); ok {
		t.Errorf("expected no result when the only candidate is avoided")
	}
}

func TestFallbackEmoji(t *testing.T) {
	t.Parallel()
	for range 20 {
		e := FallbackEmoji(nil)
		if !utf8.ValidString(e) || strings.TrimSpace(e) == "" {
			t.Errorf("invalid fallback emoji: %q", e)
		}
	}
}

func TestWordEmojisAllValid(t *testing.T) {
	t.Parallel()
	for w, e := range wordEmojis {
		if !utf8.ValidString(e) || e == "" {
			t.Errorf("invalid emoji for word %q: %q", w, e)
		}
	}
}

func TestWordsCoveredByEmojis(t *testing.T) {
	t.Parallel()
	// Every word in the names vocabulary should have an emoji mapping. If this
	// test fires, add the new word to wordEmojis.
	for _, w := range words {
		if _, ok := wordEmojis[w]; !ok {
			t.Errorf("word %q in name vocabulary has no emoji mapping", w)
		}
	}
}
