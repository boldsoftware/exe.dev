package llmpricing

import (
	"math"
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
			model: Claude45Opus,
			usage: Usage{
				InputTokens:  1_000_000,
				OutputTokens: 1_000_000,
			},
			wantUSD:  30.0, // $5 input + $25 output
			wantDesc: "1M input @ $5 + 1M output @ $25 = $30",
		},
		{
			name:  "claude-sonnet-4.5 with cache",
			model: Claude45Sonnet,
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
			name:  "claude-3-haiku cheap",
			model: Claude3Haiku,
			usage: Usage{
				InputTokens:  10_000_000,
				OutputTokens: 1_000_000,
			},
			wantUSD:  3.75, // $2.50 + $1.25
			wantDesc: "10M input @ $0.25 + 1M output @ $1.25 = $3.75",
		},
		{
			name:  "claude-3-opus expensive",
			model: Claude3Opus,
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
			got := CalculateCost(tt.model, tt.usage)
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
			name:  "gpt-4 expensive",
			model: "gpt-4",
			usage: Usage{
				InputTokens:  1_000_000,
				OutputTokens: 1_000_000,
			},
			wantUSD: 90.0, // $30 + $60
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CalculateCost(tt.model, tt.usage)
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
			wantUSD: 1.8, // $0.90 + $0.90
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
			model: "accounts/fireworks/models/deepseek-r1",
			usage: Usage{
				InputTokens:  1_000_000,
				OutputTokens: 1_000_000,
			},
			wantUSD: 11.0, // $3 + $8
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CalculateCost(tt.model, tt.usage)
			if !floatEqual(got, tt.wantUSD) {
				t.Errorf("CalculateCost(%s) = $%.6f, want $%.6f", tt.model, got, tt.wantUSD)
			}
		})
	}
}

func TestCalculateCost_UnknownModel(t *testing.T) {
	got := CalculateCost("unknown-model-xyz", Usage{
		InputTokens:  1_000_000,
		OutputTokens: 1_000_000,
	})
	if got != 0 {
		t.Errorf("CalculateCost(unknown) = $%.6f, want $0", got)
	}
}

func TestCalculateCost_ZeroUsage(t *testing.T) {
	got := CalculateCost(Claude45Sonnet, Usage{})
	if got != 0 {
		t.Errorf("CalculateCost(zero usage) = $%.6f, want $0", got)
	}
}

func TestCalculateCost_SmallUsage(t *testing.T) {
	// Test that small token counts don't get lost to rounding
	got := CalculateCost(Claude45Sonnet, Usage{
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
