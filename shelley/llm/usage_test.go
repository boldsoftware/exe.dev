package llm

import "testing"

func TestUsageTotalInputTokens(t *testing.T) {
	tests := []struct {
		name  string
		usage Usage
		want  uint64
	}{
		{
			name: "all token types",
			usage: Usage{
				InputTokens:              100,
				CacheCreationInputTokens: 50,
				CacheReadInputTokens:     200,
				OutputTokens:             30,
			},
			want: 350, // 100 + 50 + 200
		},
		{
			name: "only input tokens",
			usage: Usage{
				InputTokens:  150,
				OutputTokens: 50,
			},
			want: 150,
		},
		{
			name: "heavy caching",
			usage: Usage{
				InputTokens:              10,
				CacheCreationInputTokens: 0,
				CacheReadInputTokens:     5000,
				OutputTokens:             100,
			},
			want: 5010, // 10 + 0 + 5000
		},
		{
			name:  "zero",
			usage: Usage{},
			want:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.usage.TotalInputTokens()
			if got != tt.want {
				t.Errorf("TotalInputTokens() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestUsageContextWindowUsed(t *testing.T) {
	tests := []struct {
		name  string
		usage Usage
		want  uint64
	}{
		{
			name: "all token types",
			usage: Usage{
				InputTokens:              100,
				CacheCreationInputTokens: 50,
				CacheReadInputTokens:     200,
				OutputTokens:             30,
			},
			want: 380, // 100 + 50 + 200 + 30
		},
		{
			name: "only input and output",
			usage: Usage{
				InputTokens:  150,
				OutputTokens: 50,
			},
			want: 200,
		},
		{
			name: "heavy caching with output",
			usage: Usage{
				InputTokens:              10,
				CacheCreationInputTokens: 0,
				CacheReadInputTokens:     5000,
				OutputTokens:             100,
			},
			want: 5110, // 10 + 0 + 5000 + 100
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.usage.ContextWindowUsed()
			if got != tt.want {
				t.Errorf("ContextWindowUsed() = %d, want %d", got, tt.want)
			}
		})
	}
}
