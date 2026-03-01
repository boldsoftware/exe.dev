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
