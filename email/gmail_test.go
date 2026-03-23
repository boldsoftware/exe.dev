package email

import "testing"

func TestIsGmailAddress(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"user@gmail.com", true},
		{"user@Gmail.Com", true},
		{"user@googlemail.com", true},
		{"user@example.com", false},
		{"user@gmailx.com", false},
		{"invalid", false},
	}
	for _, tt := range tests {
		if got := IsGmailAddress(tt.input); got != tt.want {
			t.Errorf("IsGmailAddress(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestStripPlusSuffix(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		// Basic plus stripping
		{"user+tag@gmail.com", "user@gmail.com"},
		{"user+foo+bar@gmail.com", "user@gmail.com"},
		{"david+test@example.com", "david@example.com"},

		// No plus - unchanged
		{"user@gmail.com", "user@gmail.com"},
		{"user@example.com", "user@example.com"},

		// Edge cases
		{"+tag@gmail.com", "@gmail.com"},
		{"user+@gmail.com", "user@gmail.com"},

		// No @ - returned unchanged
		{"invalid", "invalid"},

		// Plus in domain (shouldn't happen but be safe)
		{"user@gm+ail.com", "user@gm+ail.com"},
	}
	for _, tt := range tests {
		if got := StripPlusSuffix(tt.input); got != tt.want {
			t.Errorf("StripPlusSuffix(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestStripDots(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"a.b.c@gmail.com", "abc@gmail.com"},
		{"user@gmail.com", "user@gmail.com"},
		{"u.s.e.r@example.com", "user@example.com"},
		{"no-at-sign", "no-at-sign"},
	}
	for _, tt := range tests {
		if got := StripDots(tt.input); got != tt.want {
			t.Errorf("StripDots(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestGmailEqual(t *testing.T) {
	tests := []struct {
		a, b string
		want bool
	}{
		// Dots only
		{"abc.def@gmail.com", "abcdef@gmail.com", true},
		{"a.b.c@gmail.com", "abc@gmail.com", true},

		// Plus only
		{"user+tag@gmail.com", "user@gmail.com", true},

		// Dots + plus
		{"a.b.c+tag@gmail.com", "abc@gmail.com", true},

		// Case insensitive
		{"User@Gmail.com", "user@gmail.com", true},

		// Actually different
		{"alice@gmail.com", "bob@gmail.com", false},

		// Different domains
		{"user@gmail.com", "user@example.com", false},
	}
	for _, tt := range tests {
		if got := GmailEqual(tt.a, tt.b); got != tt.want {
			t.Errorf("GmailEqual(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
}
