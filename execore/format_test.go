package execore

import "testing"

func TestFmtBytes(t *testing.T) {
	tests := []struct {
		input uint64
		want  string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1024, "1 KB"},
		{1536, "1.5 KB"},
		{1024 * 1024, "1 MB"},
		{1024 * 1024 * 512, "512 MB"},
		{1024 * 1024 * 1024, "1 GB"},
		{1024 * 1024 * 1024 * 10, "10 GB"},
		{1024 * 1024 * 1024 * 25, "25 GB"},
		{uint64(1.5 * 1024 * 1024 * 1024), "1.5 GB"},
		{1024 * 1024 * 1024 * 1024, "1024 GB"},
	}
	for _, tt := range tests {
		got := fmtBytes(tt.input)
		if got != tt.want {
			t.Errorf("fmtBytes(%d) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
