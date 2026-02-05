package patchkit

import (
	"fmt"
	"go/scanner"
	"go/token"
	"slices"
	"strings"
	"unicode"

	"sketch.dev/claudetool/editbuf"
)

// A Spec specifies a single patch.
type Spec struct {
	Off int    // Byte offset to apply the replacement
	Len int    // Length of the replacement
	Src string // Original string (for debugging)
	Old string // Search string
	New string // Replacement string
}

// Unique generates a patch spec to apply op, given a unique occurrence of needle in haystack and replacement text replace.
// It reports the number of matches found for needle in haystack: 0, 1, or 2 (for any value > 1).
func Unique(haystack, needle, replace string) (*Spec, int) {
	prefix, rest, ok := strings.Cut(haystack, needle)
	if !ok {
		return nil, 0
	}
	if strings.Contains(rest, needle) {
		return nil, 2
	}
	s := &Spec{
		Off: len(prefix),
		Len: len(needle),
		Src: haystack,
		Old: needle,
		New: replace,
	}
	return s, 1
}

// minimize reduces the size of the patch by removing any shared prefix and suffix.
func (s *Spec) minimize() {
	pre := commonPrefixLen(s.Old, s.New)
	s.Off += pre
	s.Len -= pre
	s.Old = s.Old[pre:]
	s.New = s.New[pre:]
	suf := commonSuffixLen(s.Old, s.New)
	s.Len -= suf
	s.Old = s.Old[:len(s.Old)-suf]
	s.New = s.New[:len(s.New)-suf]
}

// ApplyToEditBuf applies the patch to the given edit buffer.
func (s *Spec) ApplyToEditBuf(buf *editbuf.Buffer) {
	s.minimize()
	buf.Replace(s.Off, s.Off+s.Len, s.New)
}

// UniqueDedent is Unique, but with flexibility around consistent whitespace prefix changes.
// Unlike Unique, which returns a count of matches,
// UniqueDedent returns a boolean indicating whether a unique match was found.
// It is for LLMs that have a hard time reliably reproducing uniform whitespace prefixes.
// For example, they may generate 8 spaces instead of 6 for all relevant lines.
// UniqueDedent adjusts the needle's whitespace prefix to match the haystack's
// and then replaces the unique instance of needle in haystack with replacement.
func UniqueDedent(haystack, needle, replace string) (*Spec, bool) {
	// TODO: this all definitely admits of some optimization
	haystackLines := slices.Collect(strings.Lines(haystack))
	needleLines := slices.Collect(strings.Lines(needle))
	match := uniqueTrimmedLineMatch(haystackLines, needleLines)
	if match == -1 {
		return nil, false
	}
	// We now systematically adjust needle's whitespace prefix to match haystack.
	// The first line gets special treatment, because its leading whitespace is irrelevant,
	// and models often skip past it (or part of it).
	if len(needleLines) == 0 {
		return nil, false
	}
	// First line: cut leading whitespace and make corresponding fixes to replacement.
	// The leading whitespace will come out in the wash in Unique.
	// We need to make corresponding fixes to the replacement.
	nl0 := needleLines[0]
	noWS := strings.TrimLeftFunc(nl0, unicode.IsSpace)
	ws0, _ := strings.CutSuffix(nl0, noWS) // can't fail
	rest, ok := strings.CutPrefix(replace, ws0)
	if ok {
		// Adjust needle and replacement in tandem.
		nl0 = noWS
		replace = rest
	}
	// Calculate common whitespace prefixes for the rest.
	haystackPrefix := commonWhitespacePrefix(haystackLines[match : match+len(needleLines)])
	needlePrefix := commonWhitespacePrefix(needleLines[1:])
	nbuf := new(strings.Builder)
	for i, line := range needleLines {
		if i == 0 {
			nbuf.WriteString(nl0)
			continue
		}
		// Allow empty (newline-only) lines not to be prefixed.
		if strings.TrimRight(line, "\n\r") == "" {
			nbuf.WriteString(line)
			continue
		}
		// Swap in haystackPrefix for needlePrefix.
		nbuf.WriteString(haystackPrefix)
		nbuf.WriteString(line[len(needlePrefix):])
	}
	// Do a replacement with our new-and-improved needle.
	needle = nbuf.String()
	spec, count := Unique(haystack, needle, replace)
	if count != 1 {
		return nil, false
	}
	return spec, true
}

type tok struct {
	pos token.Position
	tok token.Token
	lit string
}

func (t tok) String() string {
	if t.lit == "" {
		return fmt.Sprintf("%s", t.tok)
	}
	return fmt.Sprintf("%s(%q)", t.tok, t.lit)
}

func tokenize(code string) ([]tok, bool) {
	var s scanner.Scanner
	fset := token.NewFileSet()
	file := fset.AddFile("", fset.Base(), len(code))
	s.Init(file, []byte(code), nil, scanner.ScanComments)
	var tokens []tok
	for {
		pos, t, lit := s.Scan()
		if s.ErrorCount > 0 {
			return nil, false // invalid Go code (or not Go code at all)
		}
		if t == token.EOF {
			return tokens, true
		}
		tokens = append(tokens, tok{pos: fset.PositionFor(pos, false), tok: t, lit: lit})
	}
}

func tokensEqual(a, b []tok) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		at, bt := a[i], b[i]
		// positions are expected to differ
		if at.tok != bt.tok || at.lit != bt.lit {
			return false
		}
	}
	return true
}

func tokensUniqueMatch(haystack, needle []tok) int {
	// TODO: optimize
	match := -1
	for i := range haystack {
		rest := haystack[i:]
		if len(rest) < len(needle) {
			break
		}
		rest = rest[:len(needle)]
		if !tokensEqual(rest, needle) {
			continue
		}
		if match != -1 {
			return -1 // multiple matches
		}
		match = i
	}
	return match
}

// UniqueGoTokens is Unique, but with flexibility around all insignificant whitespace.
// Like UniqueDedent, it returns a boolean indicating whether a unique match was found.
// It is safe (enough) because it ensures that the needle alterations occurs only in places
// where whitespace is not semantically significant.
// In practice, this appears safe.
func UniqueGoTokens(haystack, needle, replace string) (*Spec, bool) {
	nt, ok := tokenize(needle)
	if !ok {
		return nil, false
	}
	ht, ok := tokenize(haystack)
	if !ok {
		return nil, false
	}
	match := tokensUniqueMatch(ht, nt)
	if match == -1 {
		return nil, false
	}
	matchEnd := match + len(nt) - 1
	start := ht[match].pos.Offset
	needle = haystack[start:]
	if matchEnd+1 < len(ht) {
		// todo: handle match at very end of file
		end := ht[matchEnd+1].pos.Offset
		needle = needle[:end-start]
	}
	// OK, declare this very fuzzy match to be our new needle.
	spec, count := Unique(haystack, needle, replace)
	if count != 1 {
		return nil, false
	}
	return spec, true
}

// UniqueInValidGo is Unique, but with flexibility around all leading and trailing whitespace.
// Like UniqueDedent, it returns a boolean indicating whether a unique match was found.
// It is safe (enough) because it ensures that the needle alterations occurs only in places
// where whitespace is not semantically significant.
// In practice, this appears safe.
func UniqueInValidGo(haystack, needle, replace string) (*Spec, bool) {
	haystackLines := slices.Collect(strings.Lines(haystack))
	needleLines := slices.Collect(strings.Lines(needle))
	matchStart := uniqueTrimmedLineMatch(haystackLines, needleLines)
	if matchStart == -1 {
		return nil, false
	}
	needle, replace = improveNeedle(haystack, needle, replace, matchStart)
	matchEnd := matchStart + strings.Count(needle, "\n")
	// Ensure that none of the lines that we fuzzy-matched involve a multiline comment or string literal.
	var s scanner.Scanner
	fset := token.NewFileSet()
	file := fset.AddFile("", fset.Base(), len(haystack))
	s.Init(file, []byte(haystack), nil, scanner.ScanComments)
	for {
		pos, tok, lit := s.Scan()
		if s.ErrorCount > 0 {
			return nil, false // invalid Go code (or not Go code at all)
		}
		if tok == token.EOF {
			break
		}
		if tok == token.SEMICOLON || !strings.Contains(lit, "\n") {
			continue
		}
		// In a token that spans multiple lines,
		// so not perfectly matching whitespace might be unsafe.
		p := fset.Position(pos)
		tokenStart := p.Line - 1 // 1-based to 0-based
		tokenEnd := tokenStart + strings.Count(lit, "\n")
		// Check whether [matchStart, matchEnd] overlaps [tokenStart, tokenEnd]
		// TODO: think more about edge conditions here. Any off-by-one errors?
		// For example, leading whitespace and trailing whitespace
		// on this token's lines are not semantically significant.
		if tokenStart <= matchEnd && matchStart <= tokenEnd {
			// if tokenStart <= matchStart && tokenEnd <= tokenEnd {}
			return nil, false // this token overlaps the range we're replacing, not safe
		}
	}

	// TODO: restore this sanity check? it's mildly expensive and i've never seen it fail.
	// replaced := strings.Join(haystackLines[:matchStart], "") + replacement + strings.Join(haystackLines[matchEnd:], "")
	// _, err := format.Source([]byte(replaced))
	// if err != nil {
	//     return nil, false
	// }

	// OK, declare this very fuzzy match to be our new needle.
	needle = strings.Join(haystackLines[matchStart:matchEnd], "")
	spec, count := Unique(haystack, needle, replace)
	if count != 1 {
		return nil, false
	}
	return spec, true
}

// UniqueTrim is Unique, but with flexibility to shrink old/replace in tandem.
func UniqueTrim(haystack, needle, replace string) (*Spec, bool) {
	// LLMs appear to particularly struggle with the first line of a patch.
	// If that first line is replicated in replace,
	// and removing it yields a unique match,
	// we can remove that line entirely from both.
	n0, nRest, nOK := strings.Cut(needle, "\n")
	r0, rRest, rOK := strings.Cut(replace, "\n")
	if !nOK || !rOK || n0 != r0 {
		return nil, false
	}
	spec, count := Unique(haystack, nRest, rRest)
	if count != 1 {
		return nil, false
	}
	return spec, true
}

// uniqueTrimmedLineMatch returns the index of the first line in haystack that matches needle,
// when ignoring leading and trailing whitespace.
// uniqueTrimmedLineMatch returns -1 if there is no unique match.
func uniqueTrimmedLineMatch(haystackLines, needleLines []string) int {
	// TODO: optimize
	trimmedHaystackLines := trimSpaceAll(haystackLines)
	trimmedNeedleLines := trimSpaceAll(needleLines)
	match := -1
	for i := range trimmedHaystackLines {
		rest := trimmedHaystackLines[i:]
		if len(rest) < len(trimmedNeedleLines) {
			break
		}
		rest = rest[:len(trimmedNeedleLines)]
		if !slices.Equal(rest, trimmedNeedleLines) {
			continue
		}
		if match != -1 {
			return -1 // multiple matches
		}
		match = i
	}
	return match
}

func trimSpaceAll(x []string) []string {
	trimmed := make([]string, len(x))
	for i, s := range x {
		trimmed[i] = strings.TrimSpace(s)
	}
	return trimmed
}

// improveNeedle adjusts both needle and replacement in tandem to better match haystack.
// Note that we adjust search and replace together.
func improveNeedle(haystack, needle, replacement string, matchLine int) (string, string) {
	// TODO: we make new slices too much
	needleLines := slices.Collect(strings.Lines(needle))
	if len(needleLines) == 0 {
		return needle, replacement
	}
	haystackLines := slices.Collect(strings.Lines(haystack))
	if matchLine+len(needleLines) > len(haystackLines) {
		// should be impossible, but just in case
		return needle, replacement
	}
	// Add trailing last-line newline if needed to better match haystack.
	if !strings.HasSuffix(needle, "\n") && strings.HasSuffix(haystackLines[matchLine+len(needleLines)-1], "\n") {
		needle += "\n"
		replacement += "\n"
	}
	// Add leading first-line prefix if needed to better match haystack.
	rest, ok := strings.CutSuffix(haystackLines[matchLine], needleLines[0])
	if ok {
		needle = rest + needle
		replacement = rest + replacement
	}
	return needle, replacement
}

func isNonSpace(r rune) bool {
	return !unicode.IsSpace(r)
}

func whitespacePrefix(s string) string {
	firstNonSpace := strings.IndexFunc(s, isNonSpace)
	return s[:max(0, firstNonSpace)] // map -1 for "not found" onto 0
}

// commonWhitespacePrefix returns the longest common whitespace prefix of the elements of x, somewhat flexibly.
func commonWhitespacePrefix(x []string) string {
	var pre string
	for i, s := range x {
		if i == 0 {
			pre = s
			continue
		}
		// ignore line endings for the moment
		// (this is just for prefixes)
		s = strings.TrimRight(s, "\n\r")
		if s == "" {
			continue
		}
		n := commonPrefixLen(pre, whitespacePrefix(s))
		if n == 0 {
			return ""
		}
		pre = pre[:n]
	}
	pre = strings.TrimRightFunc(pre, isNonSpace)
	return pre
}

// commonPrefixLen returns the length of the common prefix of a and b.
func commonPrefixLen(a, b string) int {
	// TODO: optimize, see https://go-review.googlesource.com/c/go/+/408116
	shortest := min(len(a), len(b))
	for i := range shortest {
		if a[i] != b[i] {
			return i
		}
	}
	return shortest
}

// commonSuffixLen returns the length of the common suffix of a and b.
func commonSuffixLen(a, b string) int {
	// TODO: optimize
	shortest := min(len(a), len(b))
	for i := 0; i < shortest; i++ {
		if a[len(a)-i-1] != b[len(b)-i-1] {
			return i
		}
	}
	return shortest
}
