package execore

import (
	"testing"

	"exe.dev/execore/emojidata"
)

func TestLooksLikeEmoji(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"🚀", true},
		{"🦄", true},
		{"🌮", true},
		{"🗄️", true},   // includes variation selector
		{"👨‍💻", true},  // ZWJ sequence
		{"👩🏽‍🚀", true}, // skin tone + ZWJ
		{"🇺🇸", true},   // flag (regional indicators)
		{"⭐", true},    // misc symbols
		{"⌚", true},    // misc technical
		{"✨", true},    // dingbats
		{"", false},
		{"x", false},
		{"hi", false},
		{"🚀🌮", false}, // two graphemes
		{"a🚀", false},
		{"?", false},
		{"\n", false},
	}
	for _, tc := range cases {
		if got := looksLikeEmoji(tc.in); got != tc.want {
			t.Errorf("looksLikeEmoji(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestEmojiShortcodeResolve(t *testing.T) {
	t.Helper()
	// Basic lookup via Resolve — raw glyph passes through, shortcodes resolve.
	cases := []struct {
		in, want string
		ok       bool
	}{
		{"🦆", "🦆", true},
		{":duck:", "🦆", true},
		{":rocket:", "🚀", true},
		{":does_not_exist_zz:", "", false},
	}
	for _, tc := range cases {
		got, ok := emojidata.Resolve(tc.in)
		if ok != tc.ok || got != tc.want {
			t.Errorf("Resolve(%q) = (%q, %v), want (%q, %v)", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}
