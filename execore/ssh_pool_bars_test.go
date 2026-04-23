package execore

import (
	"strings"
	"testing"
)

func TestPoolBar(t *testing.T) {
	tests := []struct {
		name       string
		used, max  float64
		suffix     string
		wantEmpty  bool
		wantSuffix string
		wantColor  string // ANSI code substring
	}{
		{"zero max returns empty", 1, 0, "x", true, "", ""},
		{"green at 50%", 2, 4, "2.0 / 4 cores", false, "2.0 / 4 cores", "\033[32m"},
		{"yellow at 75%", 3, 4, "3 / 4", false, "3 / 4", "\033[33m"},
		{"red at 95%", 3.8, 4, "3.8 / 4", false, "3.8 / 4", "\033[31m"},
		{"clamped at 100%", 5, 4, "4 / 4", false, "4 / 4", "\033[31m"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := poolBar(tt.used, tt.max, tt.suffix)
			if tt.wantEmpty {
				if result != "" {
					t.Errorf("expected empty, got %q", result)
				}
				return
			}
			if !strings.Contains(result, tt.wantSuffix) {
				t.Errorf("expected suffix %q in %q", tt.wantSuffix, result)
			}
			if !strings.Contains(result, tt.wantColor) {
				t.Errorf("expected color %q in %q", tt.wantColor, result)
			}
			// Should contain both filled and empty block chars.
			if !strings.Contains(result, "\u2588") {
				t.Errorf("expected filled blocks in %q", result)
			}
		})
	}
}

func TestPoolBarBytes(t *testing.T) {
	gb := uint64(1024 * 1024 * 1024)
	result := poolBarBytes(4*gb, 8*gb, "")
	if result == "" {
		t.Fatal("expected non-empty result")
	}
	if !strings.Contains(result, "4 GB") {
		t.Errorf("expected '4 GB' in %q", result)
	}
	if !strings.Contains(result, "8 GB") {
		t.Errorf("expected '8 GB' in %q", result)
	}
}

func TestClampU64(t *testing.T) {
	if clampU64(10, 5) != 5 {
		t.Error("expected clamp to max")
	}
	if clampU64(3, 5) != 3 {
		t.Error("expected no clamp")
	}
}
