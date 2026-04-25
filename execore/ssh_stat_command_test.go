package execore

import "testing"

func TestParseStatRange(t *testing.T) {
	tests := []struct {
		input   string
		want    int
		wantErr bool
	}{
		{"24h", 24, false},
		{"24H", 24, false},
		{"7d", 168, false},
		{"7D", 168, false},
		{"30d", 720, false},
		{"30D", 720, false},
		{"1h", 0, true},
		{"48h", 0, true},
		{"", 0, true},
	}
	for _, tt := range tests {
		got, err := parseStatRange(tt.input)
		if tt.wantErr {
			if err == nil {
				t.Errorf("parseStatRange(%q): want error, got %d", tt.input, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseStatRange(%q): unexpected error: %v", tt.input, err)
			continue
		}
		if got != tt.want {
			t.Errorf("parseStatRange(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestSparkline(t *testing.T) {
	// Empty input.
	got := sparkline(nil)
	if got == "" {
		t.Error("sparkline(nil) should not be empty")
	}

	// Constant values should produce flat sparkline.
	got = sparkline([]float64{5, 5, 5, 5})
	for _, r := range got {
		if r != '▁' {
			t.Errorf("sparkline constant: expected all ▁, got %q", got)
			break
		}
	}

	// Ascending values.
	got = sparkline([]float64{0, 1, 2, 3, 4, 5, 6, 7})
	if len([]rune(got)) != 8 {
		t.Errorf("sparkline ascending: expected 8 chars, got %d", len([]rune(got)))
	}
	runes := []rune(got)
	if runes[0] != '▁' || runes[7] != '█' {
		t.Errorf("sparkline ascending: expected ▁ to █, got %q", got)
	}
}

func TestDownsample(t *testing.T) {
	vals := []float64{1, 2, 3, 4, 5, 6, 7, 8}

	// No downsampling needed.
	got := downsample(vals, 10)
	if len(got) != 8 {
		t.Errorf("downsample(8, 10) = %d values, want 8", len(got))
	}

	// Downsample to 4 buckets.
	got = downsample(vals, 4)
	if len(got) != 4 {
		t.Errorf("downsample(8, 4) = %d values, want 4", len(got))
	}
	// First bucket: avg(1,2) = 1.5
	if got[0] != 1.5 {
		t.Errorf("downsample bucket 0 = %f, want 1.5", got[0])
	}
}

func TestFmtGBStat(t *testing.T) {
	tests := []struct {
		input float64
		want  string
	}{
		{0, "0 MB"},
		{0.5, "512 MB"},
		{1.0, "1.0 GB"},
		{10.5, "10.5 GB"},
	}
	for _, tt := range tests {
		got := fmtGBStat(tt.input)
		if got != tt.want {
			t.Errorf("fmtGBStat(%f) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
