// Package llmpricing provides cost calculation for LLM API usage.
// It maintains an allowlist of supported models with their pricing.
package llmpricing

import (
	"fmt"
	"sort"
)

// Provider represents an LLM API provider.
type Provider string

const (
	ProviderAnthropic Provider = "anthropic"
	ProviderOpenAI    Provider = "openai"
	ProviderFireworks Provider = "fireworks"
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

// TotalInputTokens returns all input-context tokens, including cached tokens.
func (u Usage) TotalInputTokens() uint64 {
	return u.InputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens
}

// ErrModelNotAllowed is returned when a model is not in the allowlist.
var ErrModelNotAllowed = fmt.Errorf("model not allowed")

// ModelPricing returns the pricing for a model, or ErrModelNotAllowed if not found.
func ModelPricing(provider Provider, model string) (*ModelCost, error) {
	providerModels, ok := allowedModels[provider]
	if !ok {
		return nil, fmt.Errorf("%w: unknown provider %q", ErrModelNotAllowed, provider)
	}
	cost, ok := providerModels[model]
	if !ok {
		return nil, fmt.Errorf("%w: %s/%s", ErrModelNotAllowed, provider, model)
	}
	return &cost, nil
}

// IsModelAllowed checks if a model is in the allowlist for the given provider.
func IsModelAllowed(provider Provider, model string) bool {
	_, err := ModelPricing(provider, model)
	return err == nil
}

// CalculateCost returns the cost in USD for the given provider, model and usage.
// Returns 0 for unknown models (use ModelPricing to check if model is allowed first).
func CalculateCost(provider Provider, model string, usage Usage) float64 {
	return calculateCostMicroCents(provider, model, usage).USD()
}

// calculateCostMicroCents calculates cost in microCents for precision.
func calculateCostMicroCents(provider Provider, model string, usage Usage) microCents {
	cost, err := ModelPricing(provider, model)
	if err != nil {
		return 0
	}

	resolvedCost := resolveTieredCost(*cost, usage)

	total := usage.InputTokens*resolvedCost.Input +
		usage.OutputTokens*resolvedCost.Output +
		usage.CacheReadInputTokens*resolvedCost.CacheRead +
		usage.CacheCreationInputTokens*resolvedCost.CacheCreation
	return microCents(total)
}

// ModelCost holds pricing in cents per million tokens.
// Using cents (integers) avoids floating-point precision issues.
type ModelCost struct {
	Input         uint64    // cents per 1M input tokens
	Output        uint64    // cents per 1M output tokens
	CacheRead     uint64    // cents per 1M cache read tokens
	CacheCreation uint64    // cents per 1M cache creation tokens
	Type          ModelType // defaults to ModelTypeChat when empty
	Tiers         []PricingTier
}

// PricingTier overrides token pricing once total input reaches MinTotalInputTokens.
// Tiers are evaluated in ascending threshold order and resolve to a single rate
// card for the full request; they are not applied incrementally by token range.
type PricingTier struct {
	MinTotalInputTokens uint64
	Input               uint64
	Output              uint64
	CacheRead           uint64
	CacheCreation       uint64
}

// ServerToolCosts maps server tool use names to their per-use cost in microCents.
// These are flat per-request costs, not per-token.
var ServerToolCosts = map[string]microCents{
	"web_search_requests": 1_000_000, // $0.01 per search = 1 cent = 1,000,000 microCents
	// "web_fetch_requests" is not yet priced but is tracked.
}

func resolveTieredCost(cost ModelCost, usage Usage) ModelCost {
	if len(cost.Tiers) == 0 {
		return cost
	}

	resolved := cost
	totalInput := usage.TotalInputTokens()
	for _, tier := range cost.Tiers {
		if totalInput < tier.MinTotalInputTokens {
			break
		}
		resolved.Input = tier.Input
		resolved.Output = tier.Output
		resolved.CacheRead = tier.CacheRead
		resolved.CacheCreation = tier.CacheCreation
	}
	return resolved
}

// CalculateServerToolCost returns the cost in USD for server-side tool usage.
// The toolUsage map keys are tool names (e.g. "web_search_requests") and values are counts.
func CalculateServerToolCost(toolUsage map[string]uint64) float64 {
	var total microCents
	for name, count := range toolUsage {
		if costPerUse, ok := ServerToolCosts[name]; ok {
			total += microCents(count) * costPerUse
		}
	}
	return total.USD()
}

// allowedModels maps provider -> model name -> pricing.
// Only models in this map are allowed through the gateway.
//
// Prices are in cents per 1M tokens.
// Sources:
//   - Anthropic: https://www.anthropic.com/pricing#api
//   - OpenAI: https://openai.com/api/pricing/
//   - Fireworks: https://fireworks.ai/pricing
//   - models.dev: https://models.dev/api.json
var allowedModels = map[Provider]map[string]ModelCost{
	ProviderAnthropic: {
		// Claude 4.7 models
		"claude-opus-4-7": {Input: 500, Output: 2500, CacheRead: 50, CacheCreation: 625},

		// Claude 4.6 models
		"claude-opus-4-6":          {Input: 500, Output: 2500, CacheRead: 50, CacheCreation: 625},
		"claude-opus-4-6-20260115": {Input: 500, Output: 2500, CacheRead: 50, CacheCreation: 625},
		"claude-sonnet-4-6":        {Input: 300, Output: 1500, CacheRead: 30, CacheCreation: 375},

		// Claude 4.5 models
		"claude-opus-4-5-20251101":   {Input: 500, Output: 2500, CacheRead: 50, CacheCreation: 625},
		"claude-opus-4-5":            {Input: 500, Output: 2500, CacheRead: 50, CacheCreation: 625},
		"claude-sonnet-4-5-20250929": {Input: 300, Output: 1500, CacheRead: 30, CacheCreation: 375},
		"claude-sonnet-4-5":          {Input: 300, Output: 1500, CacheRead: 30, CacheCreation: 375},

		// Claude 4.0 models
		"claude-opus-4-20250514":   {Input: 1500, Output: 7500, CacheRead: 150, CacheCreation: 1875},
		"claude-opus-4-0":          {Input: 1500, Output: 7500, CacheRead: 150, CacheCreation: 1875},
		"claude-opus-4-1":          {Input: 1500, Output: 7500, CacheRead: 150, CacheCreation: 1875},
		"claude-opus-4-1-20250805": {Input: 1500, Output: 7500, CacheRead: 150, CacheCreation: 1875},
		"claude-sonnet-4-20250514": {Input: 300, Output: 1500, CacheRead: 30, CacheCreation: 375},
		"claude-sonnet-4-0":        {Input: 300, Output: 1500, CacheRead: 30, CacheCreation: 375},

		// Claude 4.5 Haiku
		"claude-haiku-4-5-20251001": {Input: 100, Output: 500, CacheRead: 10, CacheCreation: 125},
		"claude-haiku-4-5":          {Input: 100, Output: 500, CacheRead: 10, CacheCreation: 125},
	},

	ProviderOpenAI: {
		// GPT-5.5 models
		"gpt-5.5": {
			Input:     500,
			Output:    3000,
			CacheRead: 50,
			// resolveTieredCost replaces the full rate card, so tiers must repeat every non-zero rate.
			Tiers: []PricingTier{{
				MinTotalInputTokens: 272_001,
				Input:               1000,
				Output:              4500,
				CacheRead:           100,
			}},
		},
		"gpt-5.5-2026-04-23": {
			Input:     500,
			Output:    3000,
			CacheRead: 50,
			// resolveTieredCost replaces the full rate card, so tiers must repeat every non-zero rate.
			Tiers: []PricingTier{{
				MinTotalInputTokens: 272_001,
				Input:               1000,
				Output:              4500,
				CacheRead:           100,
			}},
		},
		"gpt-5.5-pro": {
			Input:  3000,
			Output: 18000,
			// resolveTieredCost replaces the full rate card, so tiers must repeat every non-zero rate.
			Tiers: []PricingTier{{
				MinTotalInputTokens: 272_001,
				Input:               6000,
				Output:              27000,
			}},
		},
		"gpt-5.5-pro-2026-04-23": {
			Input:  3000,
			Output: 18000,
			// resolveTieredCost replaces the full rate card, so tiers must repeat every non-zero rate.
			Tiers: []PricingTier{{
				MinTotalInputTokens: 272_001,
				Input:               6000,
				Output:              27000,
			}},
		},

		// GPT-5.4 models
		"gpt-5.4": {
			Input:     250,
			Output:    1500,
			CacheRead: 25,
			// resolveTieredCost replaces the full rate card, so tiers must repeat every non-zero rate.
			Tiers: []PricingTier{{
				MinTotalInputTokens: 272_001,
				Input:               500,
				Output:              2250,
				CacheRead:           50,
			}},
		},
		"gpt-5.4-2026-03-05": {
			Input:     250,
			Output:    1500,
			CacheRead: 25,
			// resolveTieredCost replaces the full rate card, so tiers must repeat every non-zero rate.
			Tiers: []PricingTier{{
				MinTotalInputTokens: 272_001,
				Input:               500,
				Output:              2250,
				CacheRead:           50,
			}},
		},
		"gpt-5.4-pro": {
			Input:  3000,
			Output: 18000,
			// resolveTieredCost replaces the full rate card, so tiers must repeat every non-zero rate.
			Tiers: []PricingTier{{
				MinTotalInputTokens: 272_001,
				Input:               6000,
				Output:              27000,
			}},
		},
		"gpt-5.4-pro-2026-03-05": {
			Input:  3000,
			Output: 18000,
			// resolveTieredCost replaces the full rate card, so tiers must repeat every non-zero rate.
			Tiers: []PricingTier{{
				MinTotalInputTokens: 272_001,
				Input:               6000,
				Output:              27000,
			}},
		},

		// GPT-5.3 models
		"gpt-5.3-codex":       {Input: 175, Output: 1400, CacheRead: 17},
		"gpt-5.3-2025-12-19":  {Input: 175, Output: 1400, CacheRead: 17},
		"gpt-5.3-chat-latest": {Input: 175, Output: 1400, CacheRead: 17},
		"gpt-5.3":             {Input: 175, Output: 1400, CacheRead: 17},
		"gpt-5.3-pro":         {Input: 2100, Output: 16800},

		// GPT-5.2 models
		"gpt-5.2-codex":       {Input: 175, Output: 1400, CacheRead: 17},
		"gpt-5.2-2025-12-11":  {Input: 175, Output: 1400, CacheRead: 17},
		"gpt-5.2-chat-latest": {Input: 175, Output: 1400, CacheRead: 17},
		"gpt-5.2":             {Input: 175, Output: 1400, CacheRead: 17},
		"gpt-5.2-pro":         {Input: 2100, Output: 16800},

		// GPT-5.1 models
		"gpt-5.1-codex":       {Input: 125, Output: 1000, CacheRead: 12},
		"gpt-5.1-2025-11-13":  {Input: 125, Output: 1000, CacheRead: 12},
		"gpt-5.1-chat-latest": {Input: 125, Output: 1000, CacheRead: 12},
		"gpt-5.1":             {Input: 125, Output: 1000, CacheRead: 12},
		"gpt-5.1-codex-max":   {Input: 125, Output: 1000, CacheRead: 12}, // Same as gpt-5.1-codex
		"gpt-5.1-codex-mini":  {Input: 25, Output: 200, CacheRead: 2},

		// GPT-5 models
		"gpt-5":                 {Input: 125, Output: 1000, CacheRead: 12},
		"gpt-5-2025-08-07":      {Input: 125, Output: 1000, CacheRead: 12},
		"gpt-5-chat-latest":     {Input: 125, Output: 1000, CacheRead: 12},
		"gpt-5-codex":           {Input: 125, Output: 1000, CacheRead: 12},
		"gpt-5-mini":            {Input: 25, Output: 200, CacheRead: 2},
		"gpt-5-mini-2025-08-07": {Input: 25, Output: 200, CacheRead: 2},
		"gpt-5-nano":            {Input: 5, Output: 40, CacheRead: 0},
		"gpt-5-nano-2025-08-07": {Input: 5, Output: 40, CacheRead: 0},
		"gpt-5-pro":             {Input: 1500, Output: 12000},

		// GPT-4.1 models
		"gpt-4.1":                 {Input: 200, Output: 800, CacheRead: 50},
		"gpt-4.1-2025-04-14":      {Input: 200, Output: 800, CacheRead: 50},
		"gpt-4.1-mini":            {Input: 40, Output: 160, CacheRead: 10},
		"gpt-4.1-mini-2025-04-14": {Input: 40, Output: 160, CacheRead: 10},
		"gpt-4.1-nano":            {Input: 10, Output: 40, CacheRead: 3},
		"gpt-4.1-nano-2025-04-14": {Input: 10, Output: 40, CacheRead: 3},

		// GPT-4o models
		"gpt-4o":                 {Input: 250, Output: 1000, CacheRead: 125},
		"gpt-4o-2024-11-20":      {Input: 250, Output: 1000, CacheRead: 125},
		"gpt-4o-2024-08-06":      {Input: 250, Output: 1000, CacheRead: 125},
		"gpt-4o-2024-05-13":      {Input: 500, Output: 1500},
		"gpt-4o-mini":            {Input: 15, Output: 60, CacheRead: 7},
		"gpt-4o-mini-2024-07-18": {Input: 15, Output: 60, CacheRead: 7},

		// O-series reasoning models
		"o1":                    {Input: 1500, Output: 6000, CacheRead: 750},
		"o1-2024-12-17":         {Input: 1500, Output: 6000, CacheRead: 750},
		"o1-mini":               {Input: 110, Output: 440, CacheRead: 55},
		"o1-preview":            {Input: 1500, Output: 6000, CacheRead: 750},
		"o1-pro":                {Input: 15000, Output: 60000},
		"o3":                    {Input: 200, Output: 800, CacheRead: 50},
		"o3-2025-04-16":         {Input: 200, Output: 800, CacheRead: 50},
		"o3-mini":               {Input: 110, Output: 440, CacheRead: 27},
		"o3-mini-2025-01-31":    {Input: 110, Output: 440, CacheRead: 27},
		"o3-pro":                {Input: 2000, Output: 8000, CacheRead: 500},
		"o3-deep-research":      {Input: 1000, Output: 4000, CacheRead: 250},
		"o4-mini":               {Input: 110, Output: 440, CacheRead: 28},
		"o4-mini-2025-04-16":    {Input: 110, Output: 440, CacheRead: 28},
		"o4-mini-deep-research": {Input: 200, Output: 800, CacheRead: 50},

		// Codex models
		"codex-mini-latest": {Input: 150, Output: 600, CacheRead: 37},

		// Embedding models
		"text-embedding-3-small": {Input: 2, Type: ModelTypeEmbedding},
		"text-embedding-3-large": {Input: 13, Type: ModelTypeEmbedding},
		"text-embedding-ada-002": {Input: 10, Type: ModelTypeEmbedding},
	},

	// Fireworks AI
	// Source: https://fireworks.ai/pricing
	ProviderFireworks: {
		// Qwen models
		"accounts/fireworks/models/qwen3-8b":           {Input: 20, Output: 20, CacheRead: 10},
		"accounts/fireworks/models/qwen3-embedding-8b": {Input: 5, Type: ModelTypeEmbedding},
		"accounts/fireworks/models/qwen3-reranker-8b":  {Input: 20, Type: ModelTypeReranker},

		// GLM models
		"accounts/fireworks/models/glm-5p1": {Input: 140, Output: 440, CacheRead: 26},
		"accounts/fireworks/models/glm-5":   {Input: 100, Output: 320, CacheRead: 20},
		"accounts/fireworks/models/glm-4p7": {Input: 60, Output: 220, CacheRead: 30},

		// Kimi models
		"accounts/fireworks/models/kimi-k2p6": {Input: 95, Output: 400, CacheRead: 16},
		"accounts/fireworks/models/kimi-k2p5": {Input: 60, Output: 300, CacheRead: 10},

		// DeepSeek models
		"accounts/fireworks/models/deepseek-v4-pro": {Input: 174, Output: 348, CacheRead: 14},
		"accounts/fireworks/models/deepseek-v3p2":   {Input: 56, Output: 168, CacheRead: 28},
		"accounts/fireworks/models/deepseek-v3p1":   {Input: 56, Output: 168, CacheRead: 28},

		// MiniMax models
		"accounts/fireworks/models/minimax-m2p5": {Input: 30, Output: 120, CacheRead: 3},

		// GPT-OSS models
		"accounts/fireworks/models/gpt-oss-120b": {Input: 15, Output: 60, CacheRead: 1},
		"accounts/fireworks/models/gpt-oss-20b":  {Input: 7, Output: 30, CacheRead: 4},

		// Llama models
		"accounts/fireworks/models/llama-v3p3-70b-instruct": {Input: 90, Output: 90, CacheRead: 45},

		// Embedding models (no caching)
		"nomic-ai/nomic-embed-text-v1.5": {Input: 1, Type: ModelTypeEmbedding},
		"thenlper/gte-large":             {Input: 1, Type: ModelTypeEmbedding},
		"WhereIsAI/UAE-Large-V1":         {Input: 1, Type: ModelTypeEmbedding},
	},
}

// AllowedModels returns a copy of the allowed models map for inspection.
func AllowedModels() map[Provider][]string {
	result := make(map[Provider][]string)
	for provider, models := range allowedModels {
		for model := range models {
			result[provider] = append(result[provider], model)
		}
	}
	return result
}

// ModelType classifies how a model is called.
type ModelType string

const (
	ModelTypeChat      ModelType = "chat"
	ModelTypeEmbedding ModelType = "embedding"
	ModelTypeReranker  ModelType = "reranker"
)

// GatewayModel describes a single model available through the gateway.
type GatewayModel struct {
	Name     string
	Provider Provider
	Type     ModelType
}

// modelType returns the model's type, defaulting to ModelTypeChat.
func modelType(_ string, cost ModelCost) ModelType {
	if cost.Type != "" {
		return cost.Type
	}
	return ModelTypeChat
}

// GatewayModels returns all supported models sorted by provider then name.
func GatewayModels() []GatewayModel {
	var result []GatewayModel
	for provider, models := range allowedModels {
		for name, cost := range models {
			result = append(result, GatewayModel{
				Name:     name,
				Provider: provider,
				Type:     modelType(name, cost),
			})
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Provider != result[j].Provider {
			return result[i].Provider < result[j].Provider
		}
		return result[i].Name < result[j].Name
	})
	return result
}

// ModelMeta is optional per-model metadata exposed via the gateway catalog.
// It complements ModelCost with information that callers (e.g. pi) need to
// configure themselves without hardcoding model details. Cost lives in
// ModelCost; ModelMeta does not duplicate it.
type ModelMeta struct {
	DisplayName     string
	Reasoning       bool
	Inputs          []string // e.g. ["text"], ["text", "image"]
	ContextWindow   uint64
	MaxOutputTokens uint64
	Compat          ModelCompat
}

// ModelCompat captures provider-specific quirks used by OpenAI-compatible
// clients. Fields are emitted only when set.
type ModelCompat struct {
	SupportsDeveloperRole   *bool
	MaxTokensField          string
	SupportsReasoningEffort *bool
	ThinkingFormat          string
	CacheControlFormat      string
}

// fwOpenAICompat is the compat baseline shared by Fireworks chat models served
// via the OpenAI Chat Completions API. Fireworks rejects the developer role
// and the max_completion_tokens field.
var fwOpenAICompat = ModelCompat{
	SupportsDeveloperRole: new(false),
	MaxTokensField:        "max_tokens",
}

// fwOpenAIReasoningCompat extends fwOpenAICompat for reasoning models that
// emit thinking blocks in OpenAI's format.
var fwOpenAIReasoningCompat = func() ModelCompat {
	c := fwOpenAICompat
	c.ThinkingFormat = "openai"
	return c
}()

// gatewayMeta provides per-model metadata for the gateway catalog. Models
// without an entry are exposed with cost only; consumers that need richer
// info should treat the entry as not-yet-available and fall back to defaults.
var gatewayMeta = map[Provider]map[string]ModelMeta{
	ProviderFireworks: {
		"accounts/fireworks/models/qwen3-8b": {
			DisplayName: "Qwen3 8B (Fireworks)", Inputs: []string{"text"},
			ContextWindow: 40960, MaxOutputTokens: 16384, Compat: fwOpenAICompat,
		},
		"accounts/fireworks/models/glm-5p1": {
			DisplayName: "GLM 5.1 (Fireworks)", Inputs: []string{"text"},
			ContextWindow: 202752, MaxOutputTokens: 16384, Compat: fwOpenAICompat,
		},
		"accounts/fireworks/models/glm-5": {
			DisplayName: "GLM 5 (Fireworks)", Inputs: []string{"text"},
			ContextWindow: 202752, MaxOutputTokens: 16384, Compat: fwOpenAICompat,
		},
		"accounts/fireworks/models/glm-4p7": {
			DisplayName: "GLM 4.7 (Fireworks)", Inputs: []string{"text"},
			ContextWindow: 202752, MaxOutputTokens: 16384, Reasoning: true, Compat: fwOpenAIReasoningCompat,
		},
		"accounts/fireworks/models/kimi-k2p6": {
			DisplayName: "Kimi K2.6 (Fireworks)", Inputs: []string{"text", "image"},
			ContextWindow: 262144, MaxOutputTokens: 16384, Reasoning: true, Compat: fwOpenAIReasoningCompat,
		},
		"accounts/fireworks/models/kimi-k2p5": {
			DisplayName: "Kimi K2.5 (Fireworks)", Inputs: []string{"text", "image"},
			ContextWindow: 262144, MaxOutputTokens: 16384, Reasoning: true, Compat: fwOpenAIReasoningCompat,
		},
		"accounts/fireworks/models/deepseek-v4-pro": {
			DisplayName: "DeepSeek V4 Pro (Fireworks)", Inputs: []string{"text"},
			ContextWindow: 1048576, MaxOutputTokens: 16384, Compat: fwOpenAICompat,
		},
		"accounts/fireworks/models/deepseek-v3p2": {
			DisplayName: "DeepSeek V3p2 (Fireworks)", Inputs: []string{"text"},
			ContextWindow: 163840, MaxOutputTokens: 16384, Compat: fwOpenAICompat,
		},
		"accounts/fireworks/models/deepseek-v3p1": {
			DisplayName: "DeepSeek V3p1 (Fireworks)", Inputs: []string{"text"},
			ContextWindow: 163840, MaxOutputTokens: 16384, Compat: fwOpenAICompat,
		},
		"accounts/fireworks/models/minimax-m2p5": {
			DisplayName: "MiniMax M2.5 (Fireworks)", Inputs: []string{"text"},
			ContextWindow: 196608, MaxOutputTokens: 16384, Compat: fwOpenAICompat,
		},
		"accounts/fireworks/models/gpt-oss-120b": {
			DisplayName: "GPT-OSS 120B (Fireworks)", Inputs: []string{"text"},
			ContextWindow: 131072, MaxOutputTokens: 16384, Compat: fwOpenAICompat,
		},
		"accounts/fireworks/models/gpt-oss-20b": {
			DisplayName: "GPT-OSS 20B (Fireworks)", Inputs: []string{"text"},
			ContextWindow: 131072, MaxOutputTokens: 16384, Compat: fwOpenAICompat,
		},
		"accounts/fireworks/models/llama-v3p3-70b-instruct": {
			DisplayName: "Llama 3.3 70B (Fireworks)", Inputs: []string{"text"},
			ContextWindow: 131072, MaxOutputTokens: 16384, Compat: fwOpenAICompat,
		},
	},
}

// CatalogSchemaVersion is the schema version for the gateway catalog JSON.
// Bump on incompatible changes.
const CatalogSchemaVersion = 1

// Catalog is the JSON shape served at /llm-gateway-models.json. It groups
// models by provider so that callers can configure themselves with one
// provider registration per group.
type Catalog struct {
	SchemaVersion int               `json:"schemaVersion"`
	Providers     []CatalogProvider `json:"providers"`
}

// CatalogProvider describes one routing group under the gateway base URL.
// Path is the prefix to append to the gateway base to reach this provider's
// upstream API (e.g. "fireworks/inference/v1").
type CatalogProvider struct {
	ID     string         `json:"id"`
	Path   string         `json:"path"`
	API    string         `json:"api,omitempty"`
	Models []CatalogModel `json:"models"`
}

// CatalogModel describes a single model exposed by the gateway.
type CatalogModel struct {
	ID            string         `json:"id"`
	Name          string         `json:"name,omitempty"`
	Type          ModelType      `json:"type,omitempty"`
	Reasoning     bool           `json:"reasoning,omitempty"`
	Input         []string       `json:"input,omitempty"`
	ContextWindow uint64         `json:"contextWindow,omitempty"`
	MaxTokens     uint64         `json:"maxTokens,omitempty"`
	Cost          CatalogCost    `json:"cost"`
	Compat        *CatalogCompat `json:"compat,omitempty"`
}

// CatalogCost is the model's price in USD per 1M tokens.
type CatalogCost struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cacheRead"`
	CacheWrite float64 `json:"cacheWrite"`
}

// CatalogCompat is the JSON form of ModelCompat. Fields are omitted when
// unset so consumers can apply their own defaults.
type CatalogCompat struct {
	SupportsDeveloperRole   *bool  `json:"supportsDeveloperRole,omitempty"`
	MaxTokensField          string `json:"maxTokensField,omitempty"`
	SupportsReasoningEffort *bool  `json:"supportsReasoningEffort,omitempty"`
	ThinkingFormat          string `json:"thinkingFormat,omitempty"`
	CacheControlFormat      string `json:"cacheControlFormat,omitempty"`
}

// providerCatalogInfo describes the static routing info for a provider.
type providerCatalogInfo struct {
	id   string
	path string
	api  string // optional; only set when we want callers to use a specific API
}

// providerCatalog is the source of truth for how the gateway exposes each
// provider over HTTP. Path is relative to the gateway base URL.
var providerCatalog = []providerCatalogInfo{
	{id: "anthropic", path: "anthropic"},
	{id: "openai", path: "openai/v1"},
	{id: "fireworks", path: "fireworks/inference/v1", api: "openai-completions"},
}

// BuildCatalog returns the catalog of providers and models exposed by the
// gateway. It is deterministic: providers and models are sorted by ID.
func BuildCatalog() Catalog {
	cat := Catalog{SchemaVersion: CatalogSchemaVersion}
	for _, p := range providerCatalog {
		entry := CatalogProvider{ID: p.id, Path: p.path, API: p.api, Models: []CatalogModel{}}
		for name, cost := range allowedModels[Provider(p.id)] {
			entry.Models = append(entry.Models, buildCatalogModel(Provider(p.id), name, cost))
		}
		sort.Slice(entry.Models, func(i, j int) bool { return entry.Models[i].ID < entry.Models[j].ID })
		cat.Providers = append(cat.Providers, entry)
	}
	sort.Slice(cat.Providers, func(i, j int) bool { return cat.Providers[i].ID < cat.Providers[j].ID })
	return cat
}

func buildCatalogModel(provider Provider, name string, cost ModelCost) CatalogModel {
	m := CatalogModel{
		ID:   name,
		Type: modelType(name, cost),
		Cost: CatalogCost{
			Input:      centsPer1MtoUSD(cost.Input),
			Output:     centsPer1MtoUSD(cost.Output),
			CacheRead:  centsPer1MtoUSD(cost.CacheRead),
			CacheWrite: centsPer1MtoUSD(cost.CacheCreation),
		},
	}
	// Hide the default chat type to keep JSON terse; it is implied.
	if m.Type == ModelTypeChat {
		m.Type = ""
	}
	if meta, ok := gatewayMeta[provider][name]; ok {
		m.Name = meta.DisplayName
		m.Reasoning = meta.Reasoning
		m.Input = meta.Inputs
		m.ContextWindow = meta.ContextWindow
		m.MaxTokens = meta.MaxOutputTokens
		if c := catalogCompat(meta.Compat); c != nil {
			m.Compat = c
		}
	}
	return m
}

// centsPer1MtoUSD converts cents per 1M tokens to USD per 1M tokens.
func centsPer1MtoUSD(cents uint64) float64 { return float64(cents) / 100.0 }

// catalogCompat converts an internal ModelCompat to its JSON wire form.
// Returns nil when nothing has been set.
func catalogCompat(c ModelCompat) *CatalogCompat {
	if c.SupportsDeveloperRole == nil &&
		c.MaxTokensField == "" &&
		c.SupportsReasoningEffort == nil &&
		c.ThinkingFormat == "" &&
		c.CacheControlFormat == "" {
		return nil
	}
	return &CatalogCompat{
		SupportsDeveloperRole:   c.SupportsDeveloperRole,
		MaxTokensField:          c.MaxTokensField,
		SupportsReasoningEffort: c.SupportsReasoningEffort,
		ThinkingFormat:          c.ThinkingFormat,
		CacheControlFormat:      c.CacheControlFormat,
	}
}
