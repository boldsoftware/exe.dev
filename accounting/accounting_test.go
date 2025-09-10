package accounting

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestCostCalculation tests that costs are calculated correctly
func TestCostCalculation(t *testing.T) {
	tests := []struct {
		name  string
		model string
		usage Usage
	}{
		{
			name:  "claude-3-haiku",
			model: "claude-3-haiku-20240307",
			usage: Usage{InputTokens: 1000, OutputTokens: 500},
		},
		{
			name:  "claude-3-sonnet",
			model: "claude-3-5-sonnet-20241022",
			usage: Usage{InputTokens: 1000, OutputTokens: 500},
		},
		{
			name:  "gemini-pro",
			model: "gemini-1.5-pro",
			usage: Usage{InputTokens: 1000, OutputTokens: 500},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cost := UsageCost(tt.model, tt.usage)
			usd := cost.USD()
			assert.Greater(t, usd, 0.0, "Cost should be greater than 0")

			// Verify USD conversion makes sense
			assert.Greater(t, usd, 0.0, "USD cost should be greater than 0")
			assert.Less(t, usd, 10.0, "USD cost should be reasonable for test tokens")
		})
	}
}
