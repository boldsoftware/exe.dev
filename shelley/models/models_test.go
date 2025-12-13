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
		{id: "claude-haiku-4.5", wantID: "claude-haiku-4.5", wantNil: false},
		{id: "claude-opus-4.5", wantID: "claude-opus-4.5", wantNil: false},
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
	if d.ID != "claude-opus-4.5" {
		t.Errorf("Default().ID = %q, want %q", d.ID, "claude-opus-4.5")
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

func TestManagerGetAvailableModelsOrder(t *testing.T) {
	// Test that GetAvailableModels returns models in consistent order
	cfg := &Config{}

	// Create manager - should only have predictable model since no API keys
	manager, err := NewManager(cfg, nil)
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	// Get available models multiple times
	firstCall := manager.GetAvailableModels()
	secondCall := manager.GetAvailableModels()
	thirdCall := manager.GetAvailableModels()

	// Should return at least predictable model
	if len(firstCall) == 0 {
		t.Fatal("expected at least one model")
	}

	// All calls should return identical order
	if len(firstCall) != len(secondCall) || len(firstCall) != len(thirdCall) {
		t.Errorf("calls returned different lengths: %d, %d, %d", len(firstCall), len(secondCall), len(thirdCall))
	}

	for i := range firstCall {
		if firstCall[i] != secondCall[i] {
			t.Errorf("call 1 and 2 differ at index %d: %q vs %q", i, firstCall[i], secondCall[i])
		}
		if firstCall[i] != thirdCall[i] {
			t.Errorf("call 1 and 3 differ at index %d: %q vs %q", i, firstCall[i], thirdCall[i])
		}
	}
}

func TestManagerGetAvailableModelsMatchesAllOrder(t *testing.T) {
	// Test that available models are returned in the same order as All()
	cfg := &Config{
		AnthropicAPIKey: "test-key",
		OpenAIAPIKey:    "test-key",
		GeminiAPIKey:    "test-key",
		FireworksAPIKey: "test-key",
	}

	manager, err := NewManager(cfg, nil)
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	available := manager.GetAvailableModels()
	all := All()

	// Build expected order from All()
	var expected []string
	for _, m := range all {
		if manager.HasModel(m.ID) {
			expected = append(expected, m.ID)
		}
	}

	// Should match
	if len(available) != len(expected) {
		t.Fatalf("available models count %d != expected count %d", len(available), len(expected))
	}

	for i := range available {
		if available[i] != expected[i] {
			t.Errorf("model at index %d: got %q, want %q", i, available[i], expected[i])
		}
	}
}
