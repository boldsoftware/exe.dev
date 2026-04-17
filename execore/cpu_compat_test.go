package execore

import (
	"testing"
)

func TestMissingCPUFlags(t *testing.T) {
	tests := []struct {
		name   string
		source []string
		target []string
		want   []string
	}{
		{
			name:   "identical sets",
			source: []string{"avx2", "fpu", "sse4_2"},
			target: []string{"avx2", "fpu", "sse4_2"},
			want:   nil,
		},
		{
			name:   "target superset",
			source: []string{"avx2", "fpu"},
			target: []string{"avx2", "fpu", "sse4_2"},
			want:   nil,
		},
		{
			name:   "target missing avx512f",
			source: []string{"avx2", "avx512f", "fpu", "sse4_2"},
			target: []string{"avx2", "fpu", "sse4_2"},
			want:   []string{"avx512f"},
		},
		{
			name:   "target missing multiple",
			source: []string{"avx2", "avx512f", "avx512bw", "fpu"},
			target: []string{"avx2", "fpu"},
			want:   []string{"avx512f", "avx512bw"},
		},
		{
			name:   "both empty",
			source: []string{},
			target: []string{},
			want:   nil,
		},
		{
			name:   "source empty",
			source: []string{},
			target: []string{"avx2"},
			want:   nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := missingCPUFlags(tt.source, tt.target)
			if len(got) != len(tt.want) {
				t.Fatalf("missingCPUFlags() = %v, want %v", got, tt.want)
			}
			gotSet := make(map[string]bool, len(got))
			for _, f := range got {
				gotSet[f] = true
			}
			for _, f := range tt.want {
				if !gotSet[f] {
					t.Errorf("missing expected flag %q in result %v", f, got)
				}
			}
		})
	}
}
