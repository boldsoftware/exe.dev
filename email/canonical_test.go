package email

import (
	"testing"
)

func TestCanonicalizeEmail(t *testing.T) {
	tests := []struct {
		input   string
		want    string
		wantErr bool
	}{
		// Basic lowercase
		{"USER@EXAMPLE.COM", "user@example.com", false},
		{"User@Example.Com", "user@example.com", false},

		// Gmail aliases
		{"user@gmail.com", "user@gmail.com", false},
		{"user@googlemail.com", "user@gmail.com", false},
		{"user@google.com", "user@gmail.com", false},
		{"USER@GMAIL.COM", "user@gmail.com", false},
		{"User@GoogleMail.com", "user@gmail.com", false},

		// Outlook aliases
		{"user@outlook.com", "user@outlook.com", false},
		{"user@hotmail.com", "user@outlook.com", false},
		{"user@live.com", "user@outlook.com", false},

		// Yahoo aliases
		{"user@yahoo.com", "user@yahoo.com", false},
		{"user@ymail.com", "user@yahoo.com", false},

		// Protonmail aliases
		{"user@protonmail.com", "user@protonmail.com", false},
		{"user@proton.me", "user@protonmail.com", false},
		{"user@pm.me", "user@protonmail.com", false},

		// iCloud aliases
		{"user@icloud.com", "user@icloud.com", false},
		{"user@me.com", "user@icloud.com", false},
		{"user@mac.com", "user@icloud.com", false},

		// Fastmail aliases
		{"user@fastmail.com", "user@fastmail.com", false},
		{"user@fastmail.fm", "user@fastmail.com", false},

		// Preserve plus addressing
		{"user+tag@gmail.com", "user+tag@gmail.com", false},
		{"user+newsletter@example.com", "user+newsletter@example.com", false},

		// Preserve dots (we're not stripping them)
		{"user.name@gmail.com", "user.name@gmail.com", false},
		{"u.s.e.r@gmail.com", "u.s.e.r@gmail.com", false},

		// Whitespace trimming
		{"  user@example.com  ", "user@example.com", false},
		{"\tuser@example.com\n", "user@example.com", false},

		// Trailing dot removal
		{"user@example.com.", "user@example.com", false},

		// Invalid emails
		{"", "", true},
		{"@example.com", "", true},
		{"user@", "", true},
		{"userexample.com", "", true},
		{"user", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := CanonicalizeEmail(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("CanonicalizeEmail(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("CanonicalizeEmail(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestInvalidEmailError(t *testing.T) {
	_, err := CanonicalizeEmail("not-an-email")
	if err == nil {
		t.Fatal("expected error for invalid email")
	}
	invErr, ok := err.(*InvalidEmailError)
	if !ok {
		t.Fatalf("expected *InvalidEmailError, got %T", err)
	}
	if invErr.Input != "not-an-email" {
		t.Errorf("InvalidEmailError.Input = %q, want %q", invErr.Input, "not-an-email")
	}
	if invErr.Error() != "invalid email address: not-an-email" {
		t.Errorf("InvalidEmailError.Error() = %q", invErr.Error())
	}
}

func TestCanonicalizerCustomRule(t *testing.T) {
	c := NewCanonicalizer()

	// Add a custom rule that uppercases (silly, but tests the mechanism)
	c.AddRule("custom.com", RuleFunc(func(local string) string {
		return "prefix_" + local
	}))

	got, err := c.Canonicalize("user@custom.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "prefix_user@custom.com" {
		t.Errorf("got %q, want %q", got, "prefix_user@custom.com")
	}
}
