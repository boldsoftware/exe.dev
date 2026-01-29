package llmpricing

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"testing"
)

func TestCalculateCost_Anthropic(t *testing.T) {
	tests := []struct {
		name     string
		model    string
		usage    Usage
		wantUSD  float64
		wantDesc string
	}{
		{
			name:  "claude-opus-4.5 simple",
			model: "claude-opus-4-5-20251101",
			usage: Usage{
				InputTokens:  1_000_000,
				OutputTokens: 1_000_000,
			},
			wantUSD:  30.0, // $5 input + $25 output
			wantDesc: "1M input @ $5 + 1M output @ $25 = $30",
		},
		{
			name:  "claude-sonnet-4.5 with cache",
			model: "claude-sonnet-4-5-20250929",
			usage: Usage{
				InputTokens:              500_000,
				OutputTokens:             100_000,
				CacheCreationInputTokens: 200_000,
				CacheReadInputTokens:     300_000,
			},
			wantUSD:  3.84, // $1.50 + $1.50 + $0.75 + $0.09
			wantDesc: "500K input @ $3 + 100K output @ $15 + 200K cache write @ $3.75 + 300K cache read @ $0.30 = $3.84",
		},
		{
			name:  "claude-haiku-4-5 cheap",
			model: "claude-haiku-4-5-20251001",
			usage: Usage{
				InputTokens:  10_000_000,
				OutputTokens: 1_000_000,
			},
			wantUSD:  15.0, // $10 + $5
			wantDesc: "10M input @ $1 + 1M output @ $5 = $15",
		},
		{
			name:  "claude-opus-4 expensive",
			model: "claude-opus-4-20250514",
			usage: Usage{
				InputTokens:  1_000_000,
				OutputTokens: 500_000,
			},
			wantUSD:  52.5, // $15 + $37.50
			wantDesc: "1M input @ $15 + 500K output @ $75 = $52.50",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CalculateCost(ProviderAnthropic, tt.model, tt.usage)
			if !floatEqual(got, tt.wantUSD) {
				t.Errorf("CalculateCost(%s) = $%.6f, want $%.6f (%s)", tt.model, got, tt.wantUSD, tt.wantDesc)
			}
		})
	}
}

func TestCalculateCost_OpenAI(t *testing.T) {
	tests := []struct {
		name    string
		model   string
		usage   Usage
		wantUSD float64
	}{
		{
			name:  "gpt-4o 1M tokens each",
			model: "gpt-4o",
			usage: Usage{
				InputTokens:  1_000_000,
				OutputTokens: 1_000_000,
			},
			wantUSD: 12.5, // $2.50 + $10
		},
		{
			name:  "gpt-4o-mini cheap",
			model: "gpt-4o-mini",
			usage: Usage{
				InputTokens:  10_000_000,
				OutputTokens: 1_000_000,
			},
			wantUSD: 2.1, // $1.50 + $0.60
		},
		{
			name:  "gpt-5",
			model: "gpt-5",
			usage: Usage{
				InputTokens:  1_000_000,
				OutputTokens: 1_000_000,
			},
			wantUSD: 11.25, // $1.25 + $10
		},
		{
			name:  "gpt-5.2-codex",
			model: "gpt-5.2-codex",
			usage: Usage{
				InputTokens:  1_000_000,
				OutputTokens: 1_000_000,
			},
			wantUSD: 15.75, // $1.75 + $14
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CalculateCost(ProviderOpenAI, tt.model, tt.usage)
			if !floatEqual(got, tt.wantUSD) {
				t.Errorf("CalculateCost(%s) = $%.6f, want $%.6f", tt.model, got, tt.wantUSD)
			}
		})
	}
}

func TestCalculateCost_Fireworks(t *testing.T) {
	tests := []struct {
		name    string
		model   string
		usage   Usage
		wantUSD float64
	}{
		{
			name:  "qwen3-coder large model",
			model: "accounts/fireworks/models/qwen3-coder-480b-a35b-instruct",
			usage: Usage{
				InputTokens:  1_000_000,
				OutputTokens: 1_000_000,
			},
			wantUSD: 2.25, // $0.45 + $1.80
		},
		{
			name:  "llama-8b small model",
			model: "accounts/fireworks/models/llama-v3p1-8b-instruct",
			usage: Usage{
				InputTokens:  10_000_000,
				OutputTokens: 1_000_000,
			},
			wantUSD: 2.2, // $2.00 + $0.20
		},
		{
			name:  "deepseek-r1",
			model: "accounts/fireworks/models/deepseek-r1-0528",
			usage: Usage{
				InputTokens:  1_000_000,
				OutputTokens: 1_000_000,
			},
			wantUSD: 11.0, // $3 + $8
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CalculateCost(ProviderFireworks, tt.model, tt.usage)
			if !floatEqual(got, tt.wantUSD) {
				t.Errorf("CalculateCost(%s) = $%.6f, want $%.6f", tt.model, got, tt.wantUSD)
			}
		})
	}
}

func TestCalculateCost_UnknownModel(t *testing.T) {
	got := CalculateCost(ProviderAnthropic, "unknown-model-xyz", Usage{
		InputTokens:  1_000_000,
		OutputTokens: 1_000_000,
	})
	if got != 0 {
		t.Errorf("CalculateCost(unknown) = $%.6f, want $0", got)
	}
}

func TestCalculateCost_ZeroUsage(t *testing.T) {
	got := CalculateCost(ProviderAnthropic, "claude-sonnet-4-5-20250929", Usage{})
	if got != 0 {
		t.Errorf("CalculateCost(zero usage) = $%.6f, want $0", got)
	}
}

func TestCalculateCost_SmallUsage(t *testing.T) {
	// Test that small token counts don't get lost to rounding
	got := CalculateCost(ProviderAnthropic, "claude-sonnet-4-5-20250929", Usage{
		InputTokens:  100,
		OutputTokens: 50,
	})
	// 100 tokens @ $3/1M = $0.0003
	// 50 tokens @ $15/1M = $0.00075
	// Total = $0.00105
	want := 0.00105
	if !floatEqual(got, want) {
		t.Errorf("CalculateCost(small) = $%.8f, want $%.8f", got, want)
	}
}

func TestMicroCentsUSD(t *testing.T) {
	tests := []struct {
		mc   microCents
		want float64
	}{
		{0, 0},
		{100_000_000, 1.0}, // 1 dollar
		{1_000_000, 0.01},  // 1 cent
		{1_000, 0.00001},   // 1/100th of a cent
		{1, 0.00000001},    // 1 microCent
		{500_000_000, 5.0}, // 5 dollars
	}

	for _, tt := range tests {
		got := tt.mc.USD()
		if !floatEqual(got, tt.want) {
			t.Errorf("microCents(%d).USD() = %f, want %f", tt.mc, got, tt.want)
		}
	}
}

func TestIsModelAllowed(t *testing.T) {
	tests := []struct {
		provider Provider
		model    string
		want     bool
	}{
		{ProviderAnthropic, "claude-opus-4-5-20251101", true},
		{ProviderAnthropic, "claude-haiku-4-5", true},
		{ProviderAnthropic, "unknown-model", false},
		{ProviderOpenAI, "gpt-4o", true},
		{ProviderOpenAI, "gpt-5.2-codex", true},
		{ProviderOpenAI, "unknown-model", false},
		{ProviderFireworks, "accounts/fireworks/models/qwen3-coder-480b-a35b-instruct", true},
		{ProviderFireworks, "unknown-model", false},
		{"unknown-provider", "any-model", false},
	}

	for _, tt := range tests {
		t.Run(string(tt.provider)+"/"+tt.model, func(t *testing.T) {
			got := IsModelAllowed(tt.provider, tt.model)
			if got != tt.want {
				t.Errorf("IsModelAllowed(%q, %q) = %v, want %v", tt.provider, tt.model, got, tt.want)
			}
		})
	}
}

func TestModelPricing(t *testing.T) {
	// Test successful lookup
	cost, err := ModelPricing(ProviderAnthropic, "claude-opus-4-5-20251101")
	if err != nil {
		t.Errorf("ModelPricing(anthropic, claude-opus-4-5-20251101) error = %v", err)
	}
	if cost.Input != 500 || cost.Output != 2500 {
		t.Errorf("ModelPricing returned wrong cost: %+v", cost)
	}

	// Test unknown model
	_, err = ModelPricing(ProviderAnthropic, "unknown-model")
	if !errors.Is(err, ErrModelNotAllowed) {
		t.Errorf("ModelPricing(unknown) error = %v, want ErrModelNotAllowed", err)
	}

	// Test unknown provider
	_, err = ModelPricing("unknown-provider", "any-model")
	if !errors.Is(err, ErrModelNotAllowed) {
		t.Errorf("ModelPricing(unknown provider) error = %v, want ErrModelNotAllowed", err)
	}
}

func TestAllowedModels(t *testing.T) {
	models := AllowedModels()

	// Check that we have models for all providers
	if len(models[ProviderAnthropic]) == 0 {
		t.Error("No Anthropic models in allowlist")
	}
	if len(models[ProviderOpenAI]) == 0 {
		t.Error("No OpenAI models in allowlist")
	}
	if len(models[ProviderFireworks]) == 0 {
		t.Error("No Fireworks models in allowlist")
	}
}

// floatEqual checks if two floats are approximately equal (within 0.01%)
func floatEqual(a, b float64) bool {
	if a == b {
		return true
	}
	diff := math.Abs(a - b)
	avg := (math.Abs(a) + math.Abs(b)) / 2
	if avg == 0 {
		return diff < 1e-10
	}
	return diff/avg < 0.0001 // 0.01% tolerance
}

// TestPricingMatchesModelsDev compares our pricing against models.dev/api.json
// This test fetches the API and checks that our prices match exactly.
func TestPricingMatchesModelsDev(t *testing.T) {
	resp, err := http.Get("https://models.dev/api.json")
	if err != nil {
		t.Skipf("Failed to fetch models.dev API: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Skipf("models.dev API returned status %d", resp.StatusCode)
	}

	// Parse the response - it's a map of provider -> {models: {modelName: {cost: {input, output, ...}}}}
	type ModelInfo struct {
		Cost struct {
			Input      float64 `json:"input"`
			Output     float64 `json:"output"`
			CacheRead  float64 `json:"cache_read"`
			CacheWrite float64 `json:"cache_write"`
		} `json:"cost"`
	}
	type ProviderInfo struct {
		Models map[string]ModelInfo `json:"models"`
	}
	var apiData map[string]ProviderInfo
	if err := json.NewDecoder(resp.Body).Decode(&apiData); err != nil {
		t.Fatalf("Failed to decode models.dev API: %v", err)
	}

	// Map our provider names to models.dev provider names
	providerMap := map[Provider]string{
		ProviderAnthropic: "anthropic",
		ProviderOpenAI:    "openai",
		ProviderFireworks: "fireworks-ai",
	}

	var mismatches []string
	var checked int

	checkedByProvider := make(map[Provider]int)

	for provider, models := range allowedModels {
		apiProviderName, ok := providerMap[provider]
		if !ok {
			continue
		}

		apiProvider, ok := apiData[apiProviderName]
		if !ok {
			t.Logf("Provider %s not found in models.dev API", apiProviderName)
			continue
		}

		for modelName, ourPricing := range models {
			apiModel, ok := apiProvider.Models[modelName]
			if !ok {
				// Model not in API, skip
				continue
			}

			checked++
			checkedByProvider[provider]++

			// Convert our cents per 1M tokens to dollars per 1M tokens for comparison
			ourInputUSD := float64(ourPricing.Input) / 100.0
			ourOutputUSD := float64(ourPricing.Output) / 100.0

			// Check input price - must match exactly
			if apiModel.Cost.Input > 0 && ourInputUSD != apiModel.Cost.Input {
				mismatches = append(mismatches,
					fmt.Sprintf("%s/%s input: ours=$%.4f, api=$%.4f", provider, modelName, ourInputUSD, apiModel.Cost.Input))
			}

			// Check output price - must match exactly
			if apiModel.Cost.Output > 0 && ourOutputUSD != apiModel.Cost.Output {
				mismatches = append(mismatches,
					fmt.Sprintf("%s/%s output: ours=$%.4f, api=$%.4f", provider, modelName, ourOutputUSD, apiModel.Cost.Output))
			}
		}
	}

	t.Logf("Checked %d models against models.dev API (anthropic=%d, openai=%d, fireworks=%d)",
		checked, checkedByProvider[ProviderAnthropic], checkedByProvider[ProviderOpenAI], checkedByProvider[ProviderFireworks])

	if len(mismatches) > 0 {
		t.Errorf("Price mismatches found:\n%s", joinStrings(mismatches, "\n"))
	}
}

func joinStrings(ss []string, sep string) string {
	if len(ss) == 0 {
		return ""
	}
	result := ss[0]
	for _, s := range ss[1:] {
		result += sep + s
	}
	return result
}
