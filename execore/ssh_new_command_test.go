package execore

import (
	"slices"
	"strings"
	"testing"
)

func TestResolveNewCommandTags(t *testing.T) {
	t.Parallel()

	t.Run("unscoped_empty", func(t *testing.T) {
		tags, err := resolveNewCommandTags("", nil)
		if err != nil {
			t.Fatal(err)
		}
		if tags != nil {
			t.Fatalf("expected nil tags, got %v", tags)
		}
	})

	t.Run("unscoped_comma_separated_deduped_sorted", func(t *testing.T) {
		tags, err := resolveNewCommandTags("staging, prod,staging", nil)
		if err != nil {
			t.Fatal(err)
		}
		if want := []string{"prod", "staging"}; !slices.Equal(tags, want) {
			t.Fatalf("tags = %v, want %v", tags, want)
		}
	})

	t.Run("empty_segment_rejected", func(t *testing.T) {
		_, err := resolveNewCommandTags("prod,,staging", nil)
		if err == nil {
			t.Fatal("expected error")
		}
		if !strings.Contains(err.Error(), "empty tag") {
			t.Fatalf("expected empty-tag error, got %v", err)
		}
	})

	t.Run("invalid_tag_rejected", func(t *testing.T) {
		_, err := resolveNewCommandTags("prod,UPPER", nil)
		if err == nil {
			t.Fatal("expected error")
		}
		if !strings.Contains(err.Error(), "invalid tag name") {
			t.Fatalf("expected invalid-tag error, got %v", err)
		}
	})

	t.Run("scoped_defaults_to_scope_tag", func(t *testing.T) {
		tags, err := resolveNewCommandTags("", &SSHKeyPerms{Tag: "ci"})
		if err != nil {
			t.Fatal(err)
		}
		if want := []string{"ci"}; !slices.Equal(tags, want) {
			t.Fatalf("tags = %v, want %v", tags, want)
		}
	})

	t.Run("scoped_same_tag_allowed", func(t *testing.T) {
		tags, err := resolveNewCommandTags("ci,ci", &SSHKeyPerms{Tag: "ci"})
		if err != nil {
			t.Fatal(err)
		}
		if want := []string{"ci"}; !slices.Equal(tags, want) {
			t.Fatalf("tags = %v, want %v", tags, want)
		}
	})

	t.Run("scoped_other_tag_rejected", func(t *testing.T) {
		_, err := resolveNewCommandTags("ci,deploy", &SSHKeyPerms{Tag: "ci"})
		if err == nil {
			t.Fatal("expected error")
		}
		if !strings.Contains(err.Error(), `can only use --tag=ci`) {
			t.Fatalf("expected scope restriction error, got %v", err)
		}
	})
}
