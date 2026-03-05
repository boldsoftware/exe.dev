package execore

import "testing"

func TestShouldSaveToHistory(t *testing.T) {
	tests := []struct {
		line string
		want bool
	}{
		{"ls", true},
		{"whoami", true},
		{"new", true},
		{"exe0-to-exe1 sometoken", false},
		{"exe0-to-exe1", false},
		{"something PRIVATE KEY something", false},
		{"-----BEGIN PRIVATE KEY-----", false},
		{"", true},
	}
	for _, tt := range tests {
		if got := shouldSaveToHistory(tt.line); got != tt.want {
			t.Errorf("shouldSaveToHistory(%q) = %v, want %v", tt.line, got, tt.want)
		}
	}
}

func TestRedactCommand(t *testing.T) {
	tests := []struct {
		parts []string
		want  string
	}{
		{[]string{"ls"}, "ls"},
		{[]string{"whoami"}, "whoami"},
		{[]string{"exe0-to-exe1", "exe0.abcdef.sigblob"}, "exe0-to-exe1 exe0.[REDACTED]"},
		{[]string{"exe0-to-exe1", "--vm=mybox", "exe0.payload.sig"}, "exe0-to-exe1 --vm=mybox exe0.[REDACTED]"},
		{[]string{"exe0-to-exe1", "exe1.abcdefghijklmnop"}, "exe0-to-exe1 exe1.[REDACTED]"},
		{[]string{"something", "exe0.token", "exe1.token"}, "something exe0.[REDACTED] exe1.[REDACTED]"},
		{nil, ""},
		{[]string{}, ""},
		{[]string{"notaprefix"}, "notaprefix"},
	}
	for _, tt := range tests {
		if got := redactCommand(tt.parts); got != tt.want {
			t.Errorf("redactCommand(%v) = %q, want %q", tt.parts, got, tt.want)
		}
	}
}
