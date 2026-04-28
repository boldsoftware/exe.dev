package server

import (
	"testing"
)

func TestDaemonDefs(t *testing.T) {
	defs := daemonDefs("")

	expected := map[string]int{
		"exeprox":  3,
		"exed":     3,
		"exelet":   3,
		"metricsd": 1,
	}

	if len(defs) != len(expected) {
		t.Fatalf("expected %d daemons, got %d", len(expected), len(defs))
	}

	for _, d := range defs {
		want, ok := expected[d.Daemon]
		if !ok {
			t.Errorf("unexpected daemon %q", d.Daemon)
			continue
		}
		if len(d.Metrics) != want {
			t.Errorf("daemon %q: expected %d metrics, got %d", d.Daemon, want, len(d.Metrics))
		}
		for _, m := range d.Metrics {
			if m.Name == "" {
				t.Errorf("daemon %q: metric has empty name", d.Daemon)
			}
			if m.Query == "" {
				t.Errorf("daemon %q metric %q: empty query", d.Daemon, m.Name)
			}
			if m.InstanceQuery == "" {
				t.Errorf("daemon %q metric %q: empty instance query", d.Daemon, m.Name)
			}
			if m.Unit == "" {
				t.Errorf("daemon %q metric %q: empty unit", d.Daemon, m.Name)
			}
		}
	}
}

func TestFormatMetricValue(t *testing.T) {
	tests := []struct {
		v    float64
		unit string
		want string
	}{
		{1500000, "bytes/s", "1.5 MB/s"},
		{500, "bytes/s", "500 B/s"},
		{0.15, "req/s", "0.15 req/s"},
		{1500, "req/s", "1.5k req/s"},
		{0.005, "seconds", "5ms"},
		{1.23, "seconds", "1.23s"},
		{3.14, "cores", "3.14 cores"},
		{42, "count", "42"},
	}

	for _, tt := range tests {
		got := formatMetricValue(tt.v, tt.unit)
		if got != tt.want {
			t.Errorf("formatMetricValue(%v, %q) = %q, want %q", tt.v, tt.unit, got, tt.want)
		}
	}
}
