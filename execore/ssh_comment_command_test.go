package execore

import (
	"strings"
	"testing"
)

func TestValidateComment(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    string
		wantErr string // substring expected in error; "" = expect success
	}{
		{name: "empty", in: "", want: ""},
		{name: "only spaces becomes empty", in: "   ", want: ""},
		{name: "trim", in: "  hi  ", want: "hi"},
		{name: "ascii", in: "staging copy", want: "staging copy"},
		{name: "unicode", in: "café résumé 🚀", want: "café résumé 🚀"},
		{name: "newline rejected", in: "a\nb", wantErr: "newline"},
		{name: "tab rejected", in: "a\tb", wantErr: "newline"},
		{name: "control char rejected", in: "a\x01b", wantErr: "control"},
		{name: "html rejected <", in: "a<b", wantErr: "HTML"},
		{name: "html rejected &", in: "a&b", wantErr: "HTML"},
		{name: "html rejected double-quote", in: `a"b`, wantErr: "HTML"},
		{name: "html rejected single-quote", in: `a'b`, wantErr: "HTML"},
		{name: "html rejected backtick", in: "a`b", wantErr: "HTML"},
		{name: "too long", in: strings.Repeat("a", maxCommentBytes+1), wantErr: "too long"},
		{name: "invalid utf8", in: "\xff\xfe", wantErr: "UTF-8"},
		{name: "exactly max length", in: strings.Repeat("a", maxCommentBytes), want: strings.Repeat("a", maxCommentBytes)},
		{name: "bidi RLO rejected", in: "abc\u202Edef", wantErr: "bidi"},
		{name: "LRO rejected", in: "abc\u202Ddef", wantErr: "bidi"},
		{name: "PDI rejected", in: "abc\u2069def", wantErr: "bidi"},
		{name: "zero-width space rejected", in: "a\u200Bb", wantErr: "bidi"},
		{name: "zero-width joiner rejected", in: "a\u200Db", wantErr: "bidi"},
		{name: "BOM rejected", in: "a\uFEFFb", wantErr: "bidi"},
		{name: "soft hyphen rejected", in: "a\u00ADb", wantErr: "bidi"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := validateComment(tc.in)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("validateComment(%q) err=%v, want substring %q", tc.in, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("validateComment(%q) unexpected err: %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("validateComment(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
