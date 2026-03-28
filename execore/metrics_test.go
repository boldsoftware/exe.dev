package execore

import (
	"testing"
)

func TestEntityMetrics(t *testing.T) {
	t.Parallel()
	// Use a test server which sets up the database with migrations
	// Note: s.Stop() is called by the test helper's cleanup
	s := newTestServer(t)

	// Gather metrics from the server's registry
	metrics, err := s.metricsRegistry.Gather()
	if err != nil {
		t.Fatalf("failed to gather metrics: %v", err)
	}

	loginUsersFound := false
	devUsersFound := false
	vmsFound := false
	usersWithVMsFound := false
	for _, m := range metrics {
		switch m.GetName() {
		case "users_total":
			for _, metric := range m.GetMetric() {
				for _, label := range metric.GetLabel() {
					if label.GetName() == "type" {
						switch label.GetValue() {
						case "login":
							loginUsersFound = true
							if v := metric.GetGauge().GetValue(); v != 0 {
								t.Errorf("users_total{type=login} = %v, want 0", v)
							}
						case "dev":
							devUsersFound = true
							if v := metric.GetGauge().GetValue(); v != 0 {
								t.Errorf("users_total{type=dev} = %v, want 0", v)
							}
						}
					}
				}
			}
		case "vms_total":
			vmsFound = true
			if v := m.GetMetric()[0].GetGauge().GetValue(); v != 0 {
				t.Errorf("vms_total = %v, want 0", v)
			}
		case "users_with_vms_total":
			usersWithVMsFound = true
			if v := m.GetMetric()[0].GetGauge().GetValue(); v != 0 {
				t.Errorf("users_with_vms_total = %v, want 0", v)
			}
		}
	}

	if !loginUsersFound {
		t.Error("users_total{type=login} metric not found")
	}
	if !devUsersFound {
		t.Error("users_total{type=dev} metric not found")
	}
	if !vmsFound {
		t.Error("vms_total metric not found")
	}
	if !usersWithVMsFound {
		t.Error("users_with_vms_total metric not found")
	}
}
