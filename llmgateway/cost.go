package llmgateway

// TotalUsageCostUSD calculates the total cost in USD for all usage across models
func TotalUsageCostUSD(usages map[string]Usage) float64 {
	ret := float64(0.0)
	for model, usage := range usages {
		ret += UsageCost(model, usage).USD()
	}
	return ret
}

// Pricing data for different models
type centsPer1MTokens struct {
	Input         uint64
	Output        uint64
	CacheRead     uint64
	CacheCreation uint64
}

// https://www.anthropic.com/pricing#api
var modelCost = map[string]centsPer1MTokens{
	Claude4Sonnet: {
		Input:         300,  // $3
		Output:        1500, // $15
		CacheRead:     30,   // $0.30
		CacheCreation: 375,  // $3.75
	},
	Claude4Opus: {
		Input:         1500, // $15
		Output:        7500, // $75
		CacheRead:     150,  // $1.50
		CacheCreation: 1875, // $18.75
	},
	Claude37Sonnet: {
		Input:         300,  // $3
		Output:        1500, // $15
		CacheRead:     30,   // $0.30
		CacheCreation: 375,  // $3.75
	},
	Claude35Haiku: {
		Input:         80,  // $0.80
		Output:        400, // $4.00
		CacheRead:     8,   // $0.08
		CacheCreation: 100, // $1.00
	},
	Claude35Sonnet: {
		Input:         300,  // $3
		Output:        1500, // $15
		CacheRead:     30,   // $0.30
		CacheCreation: 375,  // $3.75
	},
	// Gemini 1.5 Pro pricing
	// https://ai.google.dev/pricing
	"gemini-1.5-pro": {
		Input:         125, // $1.25
		Output:        375, // $3.75
		CacheRead:     10,  // $0.10 - approximated, not in docs
		CacheCreation: 30,  // $0.30 - approximated, not in docs
	},
	// Gemini 1.5 Flash pricing
	"gemini-1.5-flash": {
		Input:         35,  // $0.35
		Output:        105, // $1.05
		CacheRead:     3,   // $0.03 - approximated, not in docs
		CacheCreation: 8,   // $0.08 - approximated, not in docs
	},
	// Qwen3-Coder on Fireworks pricing
	"accounts/fireworks/models/qwen3-coder-480b-a35b-instruct": {
		Input:  45,  // $0.45
		Output: 180, // $1.80
		// No caching support (yet?)
	},
	// Zai-GLM-4.5 on Fireworks pricing
	"accounts/fireworks/models/glm-4p5": {
		Input:  55,  // $0.55
		Output: 219, // $2.19
		// No caching support (yet?)
	},
	// Standard pricing https://openai.com/api/pricing/
	"gpt-5": {
		Input:     125,  // $1.25
		CacheRead: 12,   // $0.125
		Output:    1000, // $10
	},
	// Stadnard pricing https://openai.com/api/pricing/
	"gpt-5-mini": {
		Input:     25,  // $0.25
		CacheRead: 2,   // $0.025
		Output:    200, // $2
	},
}

type microCents uint64 // millionths of a cent

func (mc microCents) USD() float64 {
	return float64(mc) / 100_000_000.0 // 100 * 1M
}

// UsageCost calculates the cost in USD for a specific usage and model
func UsageCost(model string, usage Usage) microCents {
	cpm, ok := modelCost[model]
	if !ok {
		// Default to Sonnet pricing if model not found
		cpm = modelCost[Claude35Sonnet]
	}

	uc := usage.InputTokens*cpm.Input +
		usage.OutputTokens*cpm.Output +
		usage.CacheReadInputTokens*cpm.CacheRead +
		usage.CacheCreationInputTokens*cpm.CacheCreation
	return microCents(uc)
}
