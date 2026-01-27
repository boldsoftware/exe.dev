package stage

import "testing"

// TestResourceLimitsConstants verifies the resource limit constants are set correctly
func TestResourceLimitsConstants(t *testing.T) {
	// Verify minimums
	if MinMemory != 2*1000*1000*1000 {
		t.Errorf("MinMemory = %d, want 2GB (2000000000)", MinMemory)
	}
	if MinDisk != 4*1000*1000*1000 {
		t.Errorf("MinDisk = %d, want 4GB (4000000000)", MinDisk)
	}
	if MinCPUs != 1 {
		t.Errorf("MinCPUs = %d, want 1", MinCPUs)
	}

	// Verify support maximums
	if SupportMaxMemory != 32*1000*1000*1000 {
		t.Errorf("SupportMaxMemory = %d, want 32GB (32000000000)", SupportMaxMemory)
	}
	if SupportMaxDisk != 128*1000*1000*1000 {
		t.Errorf("SupportMaxDisk = %d, want 128GB (128000000000)", SupportMaxDisk)
	}
	if SupportMaxCPUs != 8 {
		t.Errorf("SupportMaxCPUs = %d, want 8", SupportMaxCPUs)
	}

	// Verify default CPUs in all environments
	for name, env := range map[string]Env{
		"Local":   Local(),
		"Test":    Test(),
		"Staging": Staging(),
		"Prod":    Prod(),
	} {
		if env.DefaultCPUs != 2 {
			t.Errorf("%s.DefaultCPUs = %d, want 2", name, env.DefaultCPUs)
		}
	}
}
