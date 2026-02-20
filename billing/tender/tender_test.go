package tender

import "testing"

func TestValueIsNegative(t *testing.T) {
	tests := []struct {
		name string
		v    Value
		want bool
	}{
		{
			name: "negative",
			v:    Mint(0, -1),
			want: true,
		},
		{
			name: "zero",
			v:    Zero(),
			want: false,
		},
		{
			name: "positive",
			v:    Mint(0, 1),
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.v.IsNegative(); got != tc.want {
				t.Fatalf("IsNegative() = %v, want %v", got, tc.want)
			}
		})
	}
}
