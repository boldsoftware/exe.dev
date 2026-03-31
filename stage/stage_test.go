package stage

import "testing"

// TestResourceLimitsConstants verifies the resource limit constants are set correctly
func TestResourceLimitsConstants(t *testing.T) {
	// Verify minimums (binary: powers of 1024)
	if MinMemory != 2*1024*1024*1024 {
		t.Errorf("MinMemory = %d, want 2 GiB (%d)", MinMemory, 2*1024*1024*1024)
	}
	if MinDisk != 4*1024*1024*1024 {
		t.Errorf("MinDisk = %d, want 4 GiB (%d)", MinDisk, 4*1024*1024*1024)
	}
	if MinCPUs != 1 {
		t.Errorf("MinCPUs = %d, want 1", MinCPUs)
	}

	// Verify support maximums (binary: powers of 1024)
	if SupportMaxMemory != 32*1024*1024*1024 {
		t.Errorf("SupportMaxMemory = %d, want 32 GiB (%d)", SupportMaxMemory, 32*1024*1024*1024)
	}
	if SupportMaxDisk != 128*1024*1024*1024 {
		t.Errorf("SupportMaxDisk = %d, want 128 GiB (%d)", SupportMaxDisk, 128*1024*1024*1024)
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
