package desiredstate

import (
	"testing"
)

func TestParseOverrides(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []CgroupSetting
	}{
		{"empty", "", nil},
		{"whitespace", "  \n  ", nil},
		{"single", "cpu.max:10000 100000", []CgroupSetting{{"cpu.max", "10000 100000"}}},
		{"multiple", "cpu.max:10000 100000\nmemory.high:1073741824", []CgroupSetting{
			{"cpu.max", "10000 100000"},
			{"memory.high", "1073741824"},
		}},
		{"trailing newline", "cpu.max:10000 100000\n", []CgroupSetting{{"cpu.max", "10000 100000"}}},
		{"no colon skipped", "badline", nil},
		{"empty value", "cpu.max:", []CgroupSetting{{"cpu.max", ""}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseOverrides(tt.input)
			if len(got) != len(tt.want) {
				t.Fatalf("len = %d, want %d", len(got), len(tt.want))
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("[%d] = %v, want %v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestFormatOverrides(t *testing.T) {
	tests := []struct {
		name  string
		input []CgroupSetting
		want  string
	}{
		{"nil", nil, ""},
		{"empty", []CgroupSetting{}, ""},
		{"single", []CgroupSetting{{"cpu.max", "10000 100000"}}, "cpu.max:10000 100000"},
		{"multiple", []CgroupSetting{
			{"cpu.max", "10000 100000"},
			{"memory.high", "1073741824"},
		}, "cpu.max:10000 100000\nmemory.high:1073741824"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatOverrides(tt.input)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestMergeOverrides(t *testing.T) {
	tests := []struct {
		name     string
		existing []CgroupSetting
		updates  []CgroupSetting
		want     []CgroupSetting
	}{
		{
			"add new",
			nil,
			[]CgroupSetting{{"cpu.max", "10000 100000"}},
			[]CgroupSetting{{"cpu.max", "10000 100000"}},
		},
		{
			"replace existing",
			[]CgroupSetting{{"cpu.max", "200000 100000"}},
			[]CgroupSetting{{"cpu.max", "10000 100000"}},
			[]CgroupSetting{{"cpu.max", "10000 100000"}},
		},
		{
			"remove by empty value",
			[]CgroupSetting{{"cpu.max", "200000 100000"}, {"memory.high", "1073741824"}},
			[]CgroupSetting{{"cpu.max", ""}},
			[]CgroupSetting{{"memory.high", "1073741824"}},
		},
		{
			"remove nonexistent is noop",
			[]CgroupSetting{{"memory.high", "1073741824"}},
			[]CgroupSetting{{"cpu.max", ""}},
			[]CgroupSetting{{"memory.high", "1073741824"}},
		},
		{
			"remove all",
			[]CgroupSetting{{"cpu.max", "200000 100000"}},
			[]CgroupSetting{{"cpu.max", ""}},
			nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MergeOverrides(tt.existing, tt.updates)
			if len(got) != len(tt.want) {
				t.Fatalf("len = %d, want %d; got %v", len(got), len(tt.want), got)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("[%d] = %v, want %v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestCPUFractionToMax(t *testing.T) {
	tests := []struct {
		fraction float64
		want     string
	}{
		{0.1, "10000 100000"},  // 10% of 1 core
		{1.0, "100000 100000"}, // 1 full core
		{2.5, "250000 100000"}, // 2.5 cores
		{0.01, "1000 100000"},  // 1% of 1 core
		{0.001, "100 100000"},  // 0.1% of 1 core
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := CPUFractionToMax(tt.fraction)
			if got != tt.want {
				t.Errorf("CPUFractionToMax(%v) = %q, want %q", tt.fraction, got, tt.want)
			}
		})
	}
}

func TestRoundTrip(t *testing.T) {
	original := []CgroupSetting{
		{"cpu.max", "10000 100000"},
		{"memory.high", "1073741824"},
	}
	formatted := FormatOverrides(original)
	parsed := ParseOverrides(formatted)
	if len(parsed) != len(original) {
		t.Fatalf("round-trip failed: len %d != %d", len(parsed), len(original))
	}
	for i := range parsed {
		if parsed[i] != original[i] {
			t.Errorf("round-trip [%d]: %v != %v", i, parsed[i], original[i])
		}
	}
}
