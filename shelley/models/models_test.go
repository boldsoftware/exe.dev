package models

import (
	"testing"
)

func TestAll(t *testing.T) {
	models := All()
	if len(models) == 0 {
		t.Fatal("expected at least one model")
	}

	// Verify all models have required fields
	for _, m := range models {
		if m.ID == "" {
			t.Errorf("model missing ID")
		}
		if m.Provider == "" {
			t.Errorf("model %s missing Provider", m.ID)
		}
		if m.Factory == nil {
			t.Errorf("model %s missing Factory", m.ID)
		}
	}
}

func TestByID(t *testing.T) {
	tests := []struct {
		id      string
		wantID  string
		wantNil bool
	}{
		{id: "qwen3-coder-fireworks", wantID: "qwen3-coder-fireworks", wantNil: false},
		{id: "gpt-5", wantID: "gpt-5", wantNil: false},
		{id: "claude-sonnet-4.5", wantID: "claude-sonnet-4.5", wantNil: false},
		{id: "claude-haiku-3.5", wantID: "claude-haiku-3.5", wantNil: false},
		{id: "nonexistent", wantNil: true},
	}

	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			m := ByID(tt.id)
			if tt.wantNil {
				if m != nil {
					t.Errorf("ByID(%q) = %v, want nil", tt.id, m)
				}
			} else {
				if m == nil {
					t.Fatalf("ByID(%q) = nil, want non-nil", tt.id)
				}
				if m.ID != tt.wantID {
					t.Errorf("ByID(%q).ID = %q, want %q", tt.id, m.ID, tt.wantID)
				}
			}
		})
	}
}

func TestDefault(t *testing.T) {
	d := Default()
	if d.ID != "qwen3-coder-fireworks" {
		t.Errorf("Default().ID = %q, want %q", d.ID, "qwen3-coder-fireworks")
	}
}

func TestIDs(t *testing.T) {
	ids := IDs()
	if len(ids) == 0 {
		t.Fatal("expected at least one model ID")
	}

	// Verify all IDs are unique
	seen := make(map[string]bool)
	for _, id := range ids {
		if seen[id] {
			t.Errorf("duplicate model ID: %s", id)
		}
		seen[id] = true
	}
}

func TestFactory(t *testing.T) {
	// Test that we can create services with empty config (should fail for most models)
	cfg := &Config{}

	// Predictable should work without any config
	m := ByID("predictable")
	if m == nil {
		t.Fatal("predictable model not found")
	}

	svc, err := m.Factory(cfg)
	if err != nil {
		t.Fatalf("predictable Factory() failed: %v", err)
	}
	if svc == nil {
		t.Fatal("predictable Factory() returned nil service")
	}
}
