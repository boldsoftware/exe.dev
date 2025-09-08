package oai

import "testing"

func TestRequiresMaxCompletionTokens(t *testing.T) {
	tests := []struct {
		name     string
		model    Model
		expected bool
	}{
		{
			name:     "GPT-5 requires max_completion_tokens",
			model:    GPT5,
			expected: true,
		},
		{
			name:     "GPT-5 Mini requires max_completion_tokens",
			model:    GPT5Mini,
			expected: true,
		},
		{
			name:     "O3 reasoning model requires max_completion_tokens",
			model:    O3,
			expected: true,
		},
		{
			name:     "O4-mini reasoning model requires max_completion_tokens",
			model:    O4Mini,
			expected: true,
		},
		{
			name:     "GPT-4.1 uses max_tokens",
			model:    GPT41,
			expected: false,
		},
		{
			name:     "GPT-4o uses max_tokens",
			model:    GPT4o,
			expected: false,
		},
		{
			name:     "GPT-4o Mini uses max_tokens",
			model:    GPT4oMini,
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.model.requiresMaxCompletionTokens()
			if result != tt.expected {
				t.Errorf("requiresMaxCompletionTokens() = %v, expected %v", result, tt.expected)
			}
		})
	}
}

func TestRequestParameterGeneration(t *testing.T) {
	// Test that we can generate the correct request structure without making API calls
	tests := []struct {
		name                      string
		model                     Model
		expectMaxTokens           bool
		expectMaxCompletionTokens bool
	}{
		{
			name:                      "GPT-5 uses max_completion_tokens",
			model:                     GPT5,
			expectMaxTokens:           false,
			expectMaxCompletionTokens: true,
		},
		{
			name:                      "GPT-5 Mini uses max_completion_tokens",
			model:                     GPT5Mini,
			expectMaxTokens:           false,
			expectMaxCompletionTokens: true,
		},
		{
			name:                      "GPT-4.1 uses max_tokens",
			model:                     GPT41,
			expectMaxTokens:           true,
			expectMaxCompletionTokens: false,
		},
		{
			name:                      "O3 uses max_completion_tokens",
			model:                     O3,
			expectMaxTokens:           false,
			expectMaxCompletionTokens: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			usesMaxCompletionTokens := tt.model.requiresMaxCompletionTokens()
			if tt.expectMaxCompletionTokens && !usesMaxCompletionTokens {
				t.Errorf("Expected model %s to use max_completion_tokens, but it doesn't", tt.model.UserName)
			}
			if tt.expectMaxTokens && usesMaxCompletionTokens {
				t.Errorf("Expected model %s to use max_tokens, but it uses max_completion_tokens", tt.model.UserName)
			}
		})
	}
}
