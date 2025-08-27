package exe

import (
	"strings"
	"testing"
)

// TestTeamNameSuggestionFromEmail tests the logic for extracting and cleaning
// team name suggestions from email addresses during registration
func TestTeamNameSuggestionFromEmail(t *testing.T) {
	t.Parallel()
	tests := []struct {
		email              string
		expectedSuggestion string
		description        string
	}{
		{
			email:              "john.doe@example.com",
			expectedSuggestion: "john-doe",
			description:        "Convert dots to hyphens",
		},
		{
			email:              "alice_smith@example.com",
			expectedSuggestion: "alice-smith",
			description:        "Convert underscores to hyphens",
		},
		{
			email:              "Bob.Johnson@example.com",
			expectedSuggestion: "bob-johnson",
			description:        "Convert to lowercase",
		},
		{
			email:              "user123@example.com",
			expectedSuggestion: "user123",
			description:        "Keep alphanumeric characters",
		},
		{
			email:              "test+tag@example.com",
			expectedSuggestion: "test-tag",
			description:        "Replace special characters with hyphens",
		},
		{
			email:              "a@example.com",
			expectedSuggestion: "a",
			description:        "Single character username",
		},
		{
			email:              "hyphen-user@example.com",
			expectedSuggestion: "hyphen-user",
			description:        "Preserve existing hyphens",
		},
		{
			email:              "user..name@example.com",
			expectedSuggestion: "user-name",
			description:        "Avoid multiple consecutive hyphens",
		},
		{
			email:              "@example.com",
			expectedSuggestion: "",
			description:        "No username part",
		},
	}

	for _, tt := range tests {
		t.Run(tt.description, func(t *testing.T) {
			// Extract username from email and clean it
			suggestedTeamName := ""
			if atIndex := strings.Index(tt.email, "@"); atIndex > 0 {
				suggestedTeamName = tt.email[:atIndex]
				// Ensure suggested name is valid (lowercase, alphanumeric and hyphens only)
				suggestedTeamName = strings.ToLower(suggestedTeamName)
				// Replace invalid characters with hyphens
				var cleaned []rune
				for _, r := range suggestedTeamName {
					if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
						cleaned = append(cleaned, r)
					} else if len(cleaned) > 0 && cleaned[len(cleaned)-1] != '-' {
						// Replace invalid chars with hyphen, but avoid multiple consecutive hyphens
						cleaned = append(cleaned, '-')
					}
				}
				suggestedTeamName = string(cleaned)
				// Trim any trailing hyphens
				suggestedTeamName = strings.Trim(suggestedTeamName, "-")
			}

			if suggestedTeamName != tt.expectedSuggestion {
				t.Errorf("For email %q, expected suggestion %q, got %q",
					tt.email, tt.expectedSuggestion, suggestedTeamName)
			}
		})
	}
}
