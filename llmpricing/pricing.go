// Package llmpricing provides cost calculation for LLM API usage.
package llmpricing

// Model name constants for known models.
const (
	// Anthropic models
	Claude35Haiku    = "claude-3-5-haiku-20241022"
	Claude35Sonnet   = "claude-3-5-sonnet-20241022"
	Claude35SonnetV1 = "claude-3-5-sonnet-20240620"
	Claude3Haiku     = "claude-3-haiku-20240307"
	Claude3Sonnet    = "claude-3-sonnet-20240229"
	Claude3Opus      = "claude-3-opus-20240229"
	Claude45Sonnet   = "claude-sonnet-4-5-20251022"
	Claude45Opus     = "claude-opus-4-5-20251101"
)

// microCents is millionths of a cent, used for precise cost calculations.
type microCents uint64

// USD converts microCents to USD as a float64.
func (mc microCents) USD() float64 {
	return float64(mc) / 100_000_000.0 // 100 cents * 1M
}

// Usage represents token usage for a single LLM request.
type Usage struct {
	InputTokens              uint64
	OutputTokens             uint64
	CacheCreationInputTokens uint64
	CacheReadInputTokens     uint64
}

// CalculateCost returns the cost in USD for the given model and usage.
// Returns 0 for unknown models.
func CalculateCost(model string, usage Usage) float64 {
	return calculateCostMicroCents(model, usage).USD()
}

// calculateCostMicroCents calculates cost in microCents for precision.
func calculateCostMicroCents(model string, usage Usage) microCents {
	cpm, ok := modelCost[model]
	if !ok {
		return 0
	}

	cost := usage.InputTokens*cpm.Input +
		usage.OutputTokens*cpm.Output +
		usage.CacheReadInputTokens*cpm.CacheRead +
		usage.CacheCreationInputTokens*cpm.CacheCreation
	return microCents(cost)
}

// centsPer1MTokens holds pricing in cents per million tokens.
// Using cents (integers) avoids floating-point precision issues.
type centsPer1MTokens struct {
	Input         uint64 // cents per 1M input tokens
	Output        uint64 // cents per 1M output tokens
	CacheRead     uint64 // cents per 1M cache read tokens
	CacheCreation uint64 // cents per 1M cache creation tokens
}

// modelCost maps model names to their pricing.
// Prices are in cents per 1M tokens.
//
// Sources:
//   - Anthropic: https://www.anthropic.com/pricing#api
//   - OpenAI: https://openai.com/api/pricing/
//   - Fireworks: https://fireworks.ai/pricing
var modelCost = map[string]centsPer1MTokens{
	// Anthropic Claude 4.5 models
	Claude45Opus: {
		Input:         500,  // $5.00
		Output:        2500, // $25.00
		CacheRead:     50,   // $0.50
		CacheCreation: 625,  // $6.25
	},
	Claude45Sonnet: {
		Input:         300,  // $3.00
		Output:        1500, // $15.00
		CacheRead:     30,   // $0.30
		CacheCreation: 375,  // $3.75
	},

	// Anthropic Claude 3.5 models
	Claude35Sonnet: {
		Input:         300,  // $3.00
		Output:        1500, // $15.00
		CacheRead:     30,   // $0.30
		CacheCreation: 375,  // $3.75
	},
	Claude35SonnetV1: {
		Input:         300,  // $3.00
		Output:        1500, // $15.00
		CacheRead:     30,   // $0.30
		CacheCreation: 375,  // $3.75
	},
	Claude35Haiku: {
		Input:         80,  // $0.80
		Output:        400, // $4.00
		CacheRead:     8,   // $0.08
		CacheCreation: 100, // $1.00
	},

	// Anthropic Claude 3 models
	Claude3Opus: {
		Input:         1500, // $15.00
		Output:        7500, // $75.00
		CacheRead:     150,  // $1.50
		CacheCreation: 1875, // $18.75
	},
	Claude3Sonnet: {
		Input:         300,  // $3.00
		Output:        1500, // $15.00
		CacheRead:     30,   // $0.30
		CacheCreation: 375,  // $3.75
	},
	Claude3Haiku: {
		Input:         25,   // $0.25
		Output:        125,  // $1.25
		CacheRead:     2,    // $0.025 (rounded)
		CacheCreation: 31,   // $0.3125 (rounded)
	},

	// OpenAI models
	"gpt-4o": {
		Input:     250, // $2.50
		Output:    1000, // $10.00
		CacheRead: 125, // $1.25
	},
	"gpt-4o-2024-11-20": {
		Input:     250,  // $2.50
		Output:    1000, // $10.00
		CacheRead: 125,  // $1.25
	},
	"gpt-4o-mini": {
		Input:     15,  // $0.15
		Output:    60,  // $0.60
		CacheRead: 7,   // $0.075 (rounded)
	},
	"gpt-4-turbo": {
		Input:  1000, // $10.00
		Output: 3000, // $30.00
	},
	"gpt-4": {
		Input:  3000, // $30.00
		Output: 6000, // $60.00
	},

	// Fireworks - Llama models
	"accounts/fireworks/models/llama-v3p1-405b-instruct": {
		Input:  300, // $3.00
		Output: 300, // $3.00
	},
	"accounts/fireworks/models/llama-v3p1-70b-instruct": {
		Input:  90, // $0.90
		Output: 90, // $0.90
	},
	"accounts/fireworks/models/llama-v3p1-8b-instruct": {
		Input:  20, // $0.20
		Output: 20, // $0.20
	},

	// Fireworks - Qwen models
	"accounts/fireworks/models/qwen2p5-72b-instruct": {
		Input:  90, // $0.90
		Output: 90, // $0.90
	},
	"accounts/fireworks/models/qwen3-235b-a22b": {
		Input:  90, // $0.90
		Output: 90, // $0.90
	},
	"accounts/fireworks/models/qwen3-30b-a3b": {
		Input:  20, // $0.20
		Output: 20, // $0.20
	},
	"accounts/fireworks/models/qwen3-coder-480b-a35b-instruct": {
		Input:  90, // $0.90
		Output: 90, // $0.90
	},

	// Fireworks - DeepSeek models
	"accounts/fireworks/models/deepseek-v3": {
		Input:  90, // $0.90
		Output: 90, // $0.90
	},
	"accounts/fireworks/models/deepseek-r1": {
		Input:  300, // $3.00
		Output: 800, // $8.00
	},
}
