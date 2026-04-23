package execore

import (
	"strings"
	"testing"
)

func TestFormatCompletions(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		if got := formatCompletions(nil, 80); got != "" {
			t.Errorf("want empty, got %q", got)
		}
	})

	t.Run("single row fits", func(t *testing.T) {
		got := formatCompletions([]string{"alpha", "beta", "gamma"}, 80)
		// Should start with newline and end with newline, no trailing prompt.
		if !strings.HasPrefix(got, "\n") || !strings.HasSuffix(got, "\n") {
			t.Fatalf("bad framing: %q", got)
		}
		for _, s := range []string{"alpha", "beta", "gamma"} {
			if !strings.Contains(got, s) {
				t.Errorf("missing %q in %q", s, got)
			}
		}
	})

	t.Run("no line exceeds width", func(t *testing.T) {
		items := []string{
			":dagger:", ":dancer:", ":dancers:", ":dancing_men:",
			":dancing_women:", ":dango:", ":dark_sunglasses:", ":dart:",
			":dash:", ":date:", ":de:", ":deaf_man:", ":deaf_person:",
			":deaf_woman:", ":deciduous_tree:", ":deer:", ":denmark:",
			":department_store:", ":derelict_house:", ":desert:",
			":desert_island:", ":desktop_computer:", ":detective:",
			":diamond_shape_with_a_dot_inside:",
		}
		for _, w := range []int{40, 80, 120} {
			out := formatCompletions(items, w)
			for _, line := range strings.Split(strings.Trim(out, "\n"), "\n") {
				if visualLen(line) > w {
					t.Errorf("width=%d: line %q exceeds width (%d)", w, line, visualLen(line))
				}
			}
		}
	})

	t.Run("narrow width falls back to one column", func(t *testing.T) {
		items := []string{"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxx", "y", "z"}
		out := formatCompletions(items, 10)
		lines := strings.Split(strings.Trim(out, "\n"), "\n")
		if len(lines) != 3 {
			t.Errorf("want 3 rows, got %d: %q", len(lines), out)
		}
	})
}
