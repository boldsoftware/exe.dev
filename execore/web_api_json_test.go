package execore

import "testing"

func TestFormatPoolSize(t *testing.T) {
	tests := []struct {
		cpus     uint64
		memoryGB uint64
		want     string
	}{
		{2, 8, "2 vCPUs \u00b7 8 GB memory"},
		{4, 16, "4 vCPUs \u00b7 16 GB memory"},
		{16, 64, "16 vCPUs \u00b7 64 GB memory"},
		{0, 0, ""},
	}
	for _, tt := range tests {
		got := formatPoolSize(tt.cpus, tt.memoryGB)
		if got != tt.want {
			t.Errorf("formatPoolSize(%d, %d) = %q, want %q", tt.cpus, tt.memoryGB, got, tt.want)
		}
	}
}
