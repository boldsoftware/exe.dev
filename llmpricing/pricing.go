// Package llmpricing provides cost calculation for LLM API usage.
// It maintains an allowlist of supported models with their pricing.
package llmpricing

import "fmt"

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

	total := usage.InputTokens*cost.Input +
		usage.OutputTokens*cost.Output +
		usage.CacheReadInputTokens*cost.CacheRead +
		usage.CacheCreationInputTokens*cost.CacheCreation
	return microCents(total)
}

// ModelCost holds pricing in cents per million tokens.
// Using cents (integers) avoids floating-point precision issues.
type ModelCost struct {
	Input         uint64 // cents per 1M input tokens
	Output        uint64 // cents per 1M output tokens
	CacheRead     uint64 // cents per 1M cache read tokens
	CacheCreation uint64 // cents per 1M cache creation tokens
}

// ServerToolCosts maps server tool use names to their per-use cost in microCents.
// These are flat per-request costs, not per-token.
var ServerToolCosts = map[string]microCents{
	"web_search_requests": 1_000_000, // $0.01 per search = 1 cent = 1,000,000 microCents
	// "web_fetch_requests" is not yet priced but is tracked.
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
		"text-embedding-3-small": {Input: 2, Output: 0},
		"text-embedding-3-large": {Input: 13, Output: 0},
		"text-embedding-ada-002": {Input: 10, Output: 0},
	},

	// Fireworks AI
	// Cached input tokens are priced at 50% of input price for all text and vision language models.
	// Source: https://fireworks.ai/pricing
	ProviderFireworks: {
		// Qwen models
		"accounts/fireworks/models/qwen3-coder-480b-a35b-instruct": {Input: 45, Output: 180, CacheRead: 22},
		"accounts/fireworks/models/qwen3-235b-a22b":                {Input: 22, Output: 88, CacheRead: 11},
		"accounts/fireworks/models/qwen3-8b":                       {Input: 5, Output: 20, CacheRead: 2},
		"accounts/fireworks/models/qwen3-embedding-8b":             {Input: 5, Output: 0},
		"accounts/fireworks/models/qwen3-reranker-8b":              {Input: 5, Output: 0},
		"accounts/fireworks/models/qwen3-vl-235b-a22b-thinking":    {Input: 22, Output: 88, CacheRead: 11},
		"accounts/fireworks/models/qwen2p5-vl-32b-instruct":        {Input: 20, Output: 20, CacheRead: 10},

		// GLM models
		"accounts/fireworks/models/glm-4p7":     {Input: 60, Output: 220, CacheRead: 30},
		"accounts/fireworks/models/glm-4p6":     {Input: 55, Output: 219, CacheRead: 27},
		"accounts/fireworks/models/glm-4p5":     {Input: 55, Output: 219, CacheRead: 27},
		"accounts/fireworks/models/glm-4p5-air": {Input: 22, Output: 88, CacheRead: 11},

		// Kimi models
		"accounts/fireworks/models/kimi-k2p5":             {Input: 60, Output: 300, CacheRead: 30},
		"accounts/fireworks/models/kimi-k2-thinking":      {Input: 60, Output: 250, CacheRead: 30},
		"accounts/fireworks/models/kimi-k2-instruct":      {Input: 100, Output: 300, CacheRead: 50},
		"accounts/fireworks/models/kimi-k2-instruct-0905": {Input: 100, Output: 300, CacheRead: 50},

		// DeepSeek models
		"accounts/fireworks/models/deepseek-v3p2":    {Input: 56, Output: 168, CacheRead: 28},
		"accounts/fireworks/models/deepseek-v3p1":    {Input: 56, Output: 168, CacheRead: 28},
		"accounts/fireworks/models/deepseek-v3-0324": {Input: 90, Output: 90, CacheRead: 45},
		"accounts/fireworks/models/deepseek-r1-0528": {Input: 300, Output: 800, CacheRead: 150},

		// MiniMax models
		"accounts/fireworks/models/minimax-m2":   {Input: 30, Output: 120, CacheRead: 15},
		"accounts/fireworks/models/minimax-m2p1": {Input: 30, Output: 120, CacheRead: 15},

		// GPT-OSS models
		"accounts/fireworks/models/gpt-oss-120b": {Input: 15, Output: 60, CacheRead: 7},
		"accounts/fireworks/models/gpt-oss-20b":  {Input: 5, Output: 20, CacheRead: 2},

		// Llama models
		"accounts/fireworks/models/llama-v3p3-70b-instruct":  {Input: 90, Output: 90, CacheRead: 45},
		"accounts/fireworks/models/llama-v3p1-405b-instruct": {Input: 300, Output: 300, CacheRead: 150},
		"accounts/fireworks/models/llama-v3p1-70b-instruct":  {Input: 90, Output: 90, CacheRead: 45},
		"accounts/fireworks/models/llama-v3p1-8b-instruct":   {Input: 20, Output: 20, CacheRead: 10},

		// Mixtral models
		"accounts/fireworks/models/mixtral-8x22b-instruct": {Input: 90, Output: 90, CacheRead: 45},

		// Embedding models (no caching)
		"nomic-ai/nomic-embed-text-v1.5": {Input: 1, Output: 0},
		"thenlper/gte-large":             {Input: 1, Output: 0},
		"WhereIsAI/UAE-Large-V1":         {Input: 1, Output: 0},
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
