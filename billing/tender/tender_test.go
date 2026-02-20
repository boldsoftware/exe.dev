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

func TestValueCents(t *testing.T) {
	tests := []struct {
		name string
		v    Value
		want int64
	}{
		{
			name: "positive exact cent",
			v:    Mint(100, 0),
			want: 100,
		},
		{
			name: "positive fractional microcents round up",
			v:    Mint(100, 1),
			want: 101,
		},
		{
			name: "positive fractional just below next cent rounds up",
			v:    Mint(100, 9999),
			want: 101,
		},
		{
			name: "negative exact cent",
			v:    Mint(-100, 0),
			want: -100,
		},
		{
			name: "negative fractional microcents keep truncation toward zero",
			v:    Mint(-100, -1),
			want: -100,
		},
		{
			name: "negative fractional above -1 cent keeps truncation toward zero",
			v:    Mint(0, -1),
			want: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.v.Cents(); got != tc.want {
				t.Fatalf("Cents() = %d, want %d", got, tc.want)
			}
		})
	}
}
