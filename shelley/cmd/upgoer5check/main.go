// Command upgoer5check checks that text uses only the ten hundred most
// common English words, as popularized by XKCD's "Up Goer Five."
//
// Usage:
//
//	upgoer5check [-allow word1,word2,...] [file ...]
//
// If no files are given, reads from stdin.
// Reports any words not in the allowed list.
// Exits with code 1 if violations are found.
package main

import (
	_ "embed"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"unicode"
)

//go:embed words.txt
var wordListRaw string

var allowed map[string]bool

func init() {
	allowed = make(map[string]bool)
	for _, line := range strings.Split(wordListRaw, "\n") {
		w := strings.TrimSpace(line)
		if w != "" {
			allowed[w] = true
		}
	}
}

// stems returns candidate base forms of word by stripping common
// English inflectional suffixes.
func stems(word string) []string {
	out := []string{word}

	// -ies → -y (e.g. "worries" → "worry")
	if strings.HasSuffix(word, "ies") && len(word) > 3 {
		out = append(out, word[:len(word)-3]+"y")
	}
	// -es (e.g. "watches" → "watch")
	if strings.HasSuffix(word, "es") && len(word) > 2 {
		out = append(out, word[:len(word)-2])
		out = append(out, word[:len(word)-1])
	}
	// -s (e.g. "talks" → "talk")
	if strings.HasSuffix(word, "s") && !strings.HasSuffix(word, "ss") && len(word) > 1 {
		out = append(out, word[:len(word)-1])
	}
	// -ied → -y (e.g. "worried" → "worry")
	if strings.HasSuffix(word, "ied") && len(word) > 3 {
		out = append(out, word[:len(word)-3]+"y")
	}
	// -ed (e.g. "stopped" → "stop", "closed" → "close")
	if strings.HasSuffix(word, "ed") && len(word) > 2 {
		out = append(out, word[:len(word)-2])
		out = append(out, word[:len(word)-1])
		if len(word) > 4 && word[len(word)-3] == word[len(word)-4] {
			out = append(out, word[:len(word)-3])
		}
	}
	// -ying → -ie (e.g. "tying" → "tie", "dying" → "die")
	if strings.HasSuffix(word, "ying") && len(word) > 4 {
		out = append(out, word[:len(word)-4]+"ie")
	}
	// -ing (e.g. "running" → "run", "closing" → "close")
	if strings.HasSuffix(word, "ing") && len(word) > 4 {
		out = append(out, word[:len(word)-3])
		out = append(out, word[:len(word)-3]+"e")
		if len(word) > 5 && word[len(word)-4] == word[len(word)-5] {
			out = append(out, word[:len(word)-4])
		}
	}
	// -er (e.g. "bigger" → "big", "closer" → "close")
	if strings.HasSuffix(word, "er") && len(word) > 3 {
		out = append(out, word[:len(word)-2])
		out = append(out, word[:len(word)-1])
		if len(word) > 4 && word[len(word)-3] == word[len(word)-4] {
			out = append(out, word[:len(word)-3])
		}
	}
	// -ier → -y (e.g. "busier" → "busy")
	if strings.HasSuffix(word, "ier") && len(word) > 3 {
		out = append(out, word[:len(word)-3]+"y")
	}
	// -iest → -y (e.g. "happiest" → "happy")
	if strings.HasSuffix(word, "iest") && len(word) > 4 {
		out = append(out, word[:len(word)-4]+"y")
	}
	// -est (e.g. "biggest" → "big")
	if strings.HasSuffix(word, "est") && len(word) > 4 {
		out = append(out, word[:len(word)-3])
		out = append(out, word[:len(word)-2])
		if len(word) > 5 && word[len(word)-4] == word[len(word)-5] {
			out = append(out, word[:len(word)-4])
		}
	}
	// -ly (e.g. "safely" → "safe", "softly" → "soft")
	if strings.HasSuffix(word, "ly") && len(word) > 3 {
		out = append(out, word[:len(word)-2])
		out = append(out, word[:len(word)-2]+"e")
	}
	// -ness (e.g. "darkness" → "dark")
	if strings.HasSuffix(word, "ness") && len(word) > 4 {
		out = append(out, word[:len(word)-4])
	}
	// -ful (e.g. "careful" → "care")
	if strings.HasSuffix(word, "ful") && len(word) > 3 {
		out = append(out, word[:len(word)-3])
	}
	return out
}

func isAllowed(word string) bool {
	word = strings.ToLower(word)
	if word == "" {
		return true
	}
	for _, s := range stems(word) {
		if allowed[s] {
			return true
		}
		// Second level: handle compound suffixes like "helpers" → "helper" → "help".
		// This is intentionally loose — false positives (accepting non-list words like
		// "forest" via for+est) are preferred over false negatives (rejecting valid
		// inflected prose), since the tool is a novelty linter, not a security boundary.
		for _, s2 := range stems(s) {
			if allowed[s2] {
				return true
			}
		}
	}
	return false
}

type wordInfo struct {
	word string
	col  int
}

func extractWords(line string) []wordInfo {
	runes := []rune(line)
	var words []wordInfo
	i := 0
	for i < len(runes) {
		for i < len(runes) && !unicode.IsLetter(runes[i]) && runes[i] != '\'' {
			i++
		}
		if i >= len(runes) {
			break
		}
		start := i
		for i < len(runes) && (unicode.IsLetter(runes[i]) || runes[i] == '\'') {
			i++
		}
		w := strings.Trim(string(runes[start:i]), "'")
		if w != "" {
			words = append(words, wordInfo{w, start + 1})
		}
	}
	return words
}

func main() {
	allowFlag := flag.String("allow", "", "comma-separated additional allowed words")
	flag.Parse()

	if *allowFlag != "" {
		for _, w := range strings.Split(*allowFlag, ",") {
			w = strings.TrimSpace(strings.ToLower(w))
			if w != "" {
				allowed[w] = true
			}
		}
	}

	violations := 0
	check := func(name string, r io.Reader) {
		data, err := io.ReadAll(r)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", name, err)
			os.Exit(2)
		}
		lines := strings.Split(string(data), "\n")
		seen := make(map[string]bool)
		for lineNo, line := range lines {
			for _, wi := range extractWords(line) {
				if !isAllowed(wi.word) {
					fmt.Printf("%s:%d:%d: %q\n", name, lineNo+1, wi.col, wi.word)
					violations++
					seen[strings.ToLower(wi.word)] = true
				}
			}
		}
		if len(seen) > 0 {
			sorted := make([]string, 0, len(seen))
			for w := range seen {
				sorted = append(sorted, w)
			}
			slices.Sort(sorted)
			fmt.Fprintf(os.Stderr, "\nunique bad words: %s\n", strings.Join(sorted, ", "))
		}
	}

	if flag.NArg() == 0 {
		check("<stdin>", os.Stdin)
	} else {
		for _, path := range flag.Args() {
			f, err := os.Open(path)
			if err != nil {
				fmt.Fprintf(os.Stderr, "%s: %v\n", path, err)
				os.Exit(2)
			}
			check(path, f)
			f.Close()
		}
	}

	if violations > 0 {
		fmt.Fprintf(os.Stderr, "%d violation(s)\n", violations)
		os.Exit(1)
	}
}
