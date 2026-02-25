package llmgateway

import (
	"encoding/json"
	"testing"
)

func TestEffectiveTokens(t *testing.T) {
	tests := []struct {
		name           string
		json           string
		wantPrompt     int
		wantCompletion int
		wantCached     uint64
	}{
		{
			name:           "chat completions format",
			json:           `{"id":"abc","model":"gpt-4","usage":{"prompt_tokens":100,"completion_tokens":50,"total_tokens":150}}`,
			wantPrompt:     100,
			wantCompletion: 50,
			wantCached:     0,
		},
		{
			name:           "chat completions with cache",
			json:           `{"id":"abc","model":"gpt-4","usage":{"prompt_tokens":100,"completion_tokens":50,"total_tokens":150,"prompt_tokens_details":{"cached_tokens":80}}}`,
			wantPrompt:     100,
			wantCompletion: 50,
			wantCached:     80,
		},
		{
			name:           "responses API format",
			json:           `{"id":"resp_abc","model":"gpt-5.3-codex","usage":{"input_tokens":200,"output_tokens":75,"total_tokens":275}}`,
			wantPrompt:     200,
			wantCompletion: 75,
			wantCached:     0,
		},
		{
			name:           "responses API with cache",
			json:           `{"id":"resp_abc","model":"gpt-5.3-codex","usage":{"input_tokens":200,"output_tokens":75,"total_tokens":275,"input_tokens_details":{"cached_tokens":150}}}`,
			wantPrompt:     200,
			wantCompletion: 75,
			wantCached:     150,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var oi openaiResponseUsageInfo
			if err := json.Unmarshal([]byte(tt.json), &oi); err != nil {
				t.Fatalf("json.Unmarshal: %v", err)
			}
			prompt, completion, cached := oi.effectiveTokens()
			if prompt != tt.wantPrompt {
				t.Errorf("prompt = %d, want %d", prompt, tt.wantPrompt)
			}
			if completion != tt.wantCompletion {
				t.Errorf("completion = %d, want %d", completion, tt.wantCompletion)
			}
			if cached != tt.wantCached {
				t.Errorf("cached = %d, want %d", cached, tt.wantCached)
			}
		})
	}
}
