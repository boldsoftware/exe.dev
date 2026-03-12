package execore

import (
	"testing"

	"exe.dev/stage"
)

func TestSignupAllowlistPermits(t *testing.T) {
	a := &stage.SignupAllowlist{
		Emails: []string{
			"josharian@gmail.com",
			"Alice@Example.com",
		},
		Domains: []string{
			"bold.dev",
			"chicken.exe.xyz",
		},
	}

	tests := []struct {
		email string
		want  bool
	}{
		// Exact email matches.
		{"josharian@gmail.com", true},
		{"alice@example.com", true},
		{"Alice@Example.com", true},

		// Plus-suffix variants.
		{"josharian+test@gmail.com", true},
		{"josharian+staging+extra@gmail.com", true},
		{"alice+foo@example.com", true},

		// Domain aliases (googlemail.com → gmail.com via canonicalization).
		{"josharian@googlemail.com", true},
		{"josharian+test@googlemail.com", true},

		// Domain matches.
		{"anyone@bold.dev", true},
		{"x@chicken.exe.xyz", true},
		{"UPPER@BOLD.DEV", true},

		// Non-matches.
		{"stranger@gmail.com", false},
		{"josharian@yahoo.com", false},
		{"anyone@sub.bold.dev", false},
		{"", false},
	}

	for _, tt := range tests {
		if got := signupAllowlistPermits(a, tt.email); got != tt.want {
			t.Errorf("signupAllowlistPermits(%q) = %v, want %v", tt.email, got, tt.want)
		}
	}
}

func TestSignupAllowlistPermitsNil(t *testing.T) {
	if !signupAllowlistPermits(nil, "anyone@anywhere.com") {
		t.Error("nil allowlist should allow all emails")
	}
}

func TestSignupAllowlistPermitsEmpty(t *testing.T) {
	a := &stage.SignupAllowlist{}
	if signupAllowlistPermits(a, "anyone@anywhere.com") {
		t.Error("empty allowlist should block all emails")
	}
}
