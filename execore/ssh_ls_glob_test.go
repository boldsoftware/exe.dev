package execore

import (
	"cmp"
	"regexp"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"mvdan.cc/sh/v3/pattern"
)

func TestLsGlobPattern(t *testing.T) {
	boxes := []string{"foo", "bar", "baz", "foo-test", "foo-prod", "qux"}

	tests := []struct {
		name     string
		pattern  string
		expected []string
	}{
		{
			name:     "exact match",
			pattern:  "foo",
			expected: []string{"foo"},
		},
		{
			name:     "star wildcard",
			pattern:  "foo*",
			expected: []string{"foo", "foo-test", "foo-prod"},
		},
		{
			name:     "star wildcard middle",
			pattern:  "*o*",
			expected: []string{"foo", "foo-test", "foo-prod"},
		},
		{
			name:     "question mark wildcard",
			pattern:  "ba?",
			expected: []string{"bar", "baz"},
		},
		{
			name:     "character class",
			pattern:  "[bf]*",
			expected: []string{"foo", "bar", "baz", "foo-test", "foo-prod"},
		},
		{
			name:     "no match",
			pattern:  "nomatch*",
			expected: []string{},
		},
		{
			name:     "star matches all",
			pattern:  "*",
			expected: []string{"foo", "bar", "baz", "foo-test", "foo-prod", "qux"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var filtered []string
			if pattern.HasMeta(tt.pattern, 0) {
				reStr, err := pattern.Regexp(tt.pattern, pattern.EntireString)
				assert.NoError(t, err)
				re, err := regexp.Compile(reStr)
				assert.NoError(t, err)
				for _, b := range boxes {
					if re.MatchString(b) {
						filtered = append(filtered, b)
					}
				}
			} else {
				// Literal match
				for _, b := range boxes {
					if b == tt.pattern {
						filtered = append(filtered, b)
						break
					}
				}
			}
			assert.ElementsMatch(t, tt.expected, filtered)
		})
	}
}

func TestLsMultipleArgs(t *testing.T) {
	boxes := []string{"foo", "bar", "baz", "foo-test", "foo-prod", "qux"}

	tests := []struct {
		name     string
		args     []string
		expected []string
	}{
		{
			name:     "two literal names",
			args:     []string{"foo", "bar"},
			expected: []string{"foo", "bar"},
		},
		{
			name:     "literal and glob",
			args:     []string{"qux", "ba*"},
			expected: []string{"qux", "bar", "baz"},
		},
		{
			name:     "overlapping patterns deduped",
			args:     []string{"foo*", "foo"},
			expected: []string{"foo", "foo-test", "foo-prod"},
		},
		{
			name:     "multiple globs",
			args:     []string{"foo-*", "ba?"},
			expected: []string{"foo-test", "foo-prod", "bar", "baz"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var filtered []string
			for _, arg := range tt.args {
				if pattern.HasMeta(arg, 0) {
					reStr, err := pattern.Regexp(arg, pattern.EntireString)
					assert.NoError(t, err)
					re, err := regexp.Compile(reStr)
					assert.NoError(t, err)
					for _, b := range boxes {
						if re.MatchString(b) {
							filtered = append(filtered, b)
						}
					}
				} else {
					if slices.Contains(boxes, arg) {
						filtered = append(filtered, arg)
					}
				}
			}
			slices.SortFunc(filtered, cmp.Compare)
			filtered = slices.Compact(filtered)
			assert.ElementsMatch(t, tt.expected, filtered)
		})
	}
}

func TestPatternHasMeta(t *testing.T) {
	tests := []struct {
		pattern string
		hasMeta bool
	}{
		{"foo", false},
		{"foo-bar", false},
		{"foo*", true},
		{"foo?", true},
		{"[abc]", true},
		{"foo\\*", false}, // escaped
	}

	for _, tt := range tests {
		t.Run(tt.pattern, func(t *testing.T) {
			assert.Equal(t, tt.hasMeta, pattern.HasMeta(tt.pattern, 0))
		})
	}
}
