package version

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

func TestGitBuildInfo(t *testing.T) {
	registry := prometheus.NewRegistry()
	RegisterBuildInfo(registry)

	metrics, err := registry.Gather()
	if err != nil {
		t.Fatalf("failed to gather metrics: %v", err)
	}

	found := false
	for _, m := range metrics {
		if m.GetName() == "git_build_info" {
			found = true
			if len(m.GetMetric()) == 0 {
				t.Fatal("git_build_info has no metrics")
			}
			metric := m.GetMetric()[0]
			hasCommit := false
			for _, label := range metric.GetLabel() {
				if label.GetName() == "commit" {
					hasCommit = true
					if label.GetValue() == "" {
						t.Error("commit label is empty")
					}
					t.Logf("commit label value: %s", label.GetValue())
				}
			}
			if !hasCommit {
				t.Error("git_build_info missing commit label")
			}
			if metric.GetGauge().GetValue() != 1 {
				t.Errorf("git_build_info value = %v, want 1", metric.GetGauge().GetValue())
			}
		}
	}

	if !found {
		t.Error("git_build_info metric not found")
	}
}
