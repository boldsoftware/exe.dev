package slug

import (
	"testing"
)

func TestSanitize(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"Simple Test", "simple-test"},
		{"Create a Python Script", "create-a-python-script"},
		{"Multiple   Spaces", "multiple-spaces"},
		{"Special@#$%Characters", "specialcharacters"},
		{"Under_Score_Test", "under-score-test"},
		{"--multiple-hyphens--", "multiple-hyphens"},
		{"CamelCase Example", "camelcase-example"},
		{"123 Numbers Test 456", "123-numbers-test-456"},
		{"   leading and trailing   ", "leading-and-trailing"},
		{"", ""},
		{"Very Long Slug That Might Need To Be Truncated Because It Is Too Long For Normal Use", "very-long-slug-that-might-need-to-be-truncated-because-it"},
	}

	for _, test := range tests {
		result := Sanitize(test.input)
		if result != test.expected {
			t.Errorf("Sanitize(%q) = %q, expected %q", test.input, result, test.expected)
		}
	}
}
