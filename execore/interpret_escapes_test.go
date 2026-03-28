package execore

import "testing"

func TestInterpretEscapes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"no escapes", "hello world", "hello world"},
		{"newline", `#!/bin/bash\necho hi`, "#!/bin/bash\necho hi"},
		{"multiple newlines", `line1\nline2\nline3`, "line1\nline2\nline3"},
		{"tab", `col1\tcol2`, "col1\tcol2"},
		{"escaped backslash", `path\\to\\file`, `path\to\file`},
		{"mixed", `#!/bin/bash\necho "hello\\nworld"\necho done`, "#!/bin/bash\necho \"hello\\nworld\"\necho done"},
		{"trailing backslash", `hello\`, `hello\`},
		{"unknown escape", `hello\x`, `hello\x`},
		{"empty", "", ""},
		{"just backslash-n", `\n`, "\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := interpretEscapes(tt.in)
			if got != tt.want {
				t.Errorf("interpretEscapes(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
