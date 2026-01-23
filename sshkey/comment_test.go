package sshkey

import "testing"

func TestSanitizeComment(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "empty string",
			input:    "",
			expected: "",
		},
		{
			name:     "simple name",
			input:    "my-laptop",
			expected: "my-laptop",
		},
		{
			name:     "typical ssh comment with at",
			input:    "user@hostname",
			expected: "user@hostname", // @ is safe
		},
		{
			name:     "with spaces",
			input:    "my laptop key",
			expected: "my-laptop-key",
		},
		{
			name:     "leading dash",
			input:    "-test",
			expected: "test",
		},
		{
			name:     "multiple leading dashes",
			input:    "---test",
			expected: "test",
		},
		{
			name:     "shell metacharacters",
			input:    "key;rm -rf",
			expected: "keyrm--rf",
		},
		{
			name:     "backticks and dollar",
			input:    "key`whoami`$HOME",
			expected: "keywhoamiHOME",
		},
		{
			name:     "quotes",
			input:    `key"test'name`,
			expected: "keytestname",
		},
		{
			name:     "brackets and parens",
			input:    "key[0](test){value}",
			expected: "key0testvalue",
		},
		{
			name:     "pipe and redirect",
			input:    "key|cat>/etc/passwd",
			expected: "keycatetcpasswd",
		},
		{
			name:     "truncate to 64 chars",
			input:    "this-is-a-very-very-long-ssh-key-comment-that-exceeds-64-characters",
			expected: "this-is-a-very-very-long-ssh-key-comment-that-exceeds-64-charact",
		},
		{
			name:     "collapse multiple spaces",
			input:    "my   laptop   key",
			expected: "my-laptop-key",
		},
		{
			name:     "leading and trailing spaces",
			input:    "  my-key  ",
			expected: "my-key",
		},
		{
			name:     "dash after space collapse",
			input:    "  -test",
			expected: "test",
		},
		{
			name:     "only metacharacters",
			input:    ";|$`",
			expected: "",
		},
		{
			name:     "only dashes",
			input:    "---",
			expected: "",
		},
		{
			name:     "exclamation and hash",
			input:    "key!#value",
			expected: "keyvalue",
		},
		{
			name:     "asterisk and question",
			input:    "key*.?txt",
			expected: "key.txt",
		},
		{
			name:     "tilde and slash",
			input:    "~user/key",
			expected: "userkey",
		},
		{
			name:     "internal dashes preserved",
			input:    "my--key",
			expected: "my--key",
		},
		{
			name:     "control characters removed",
			input:    "key\x00\x1f\nvalue",
			expected: "keyvalue",
		},
		{
			name:     "tab and newline removed",
			input:    "key\tname\nvalue",
			expected: "keynamevalue",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := SanitizeComment(tt.input)
			if result != tt.expected {
				t.Errorf("SanitizeComment(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}
