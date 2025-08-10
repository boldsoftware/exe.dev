package exe

import (
	"os"
	"testing"
)

func TestIsValidContainerName(t *testing.T) {
	// Create temporary database file
	tmpDB, err := os.CreateTemp("", "test_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp db: %v", err)
	}
	defer os.Remove(tmpDB.Name())
	tmpDB.Close()

	server, err := NewServer(":18080", "", ":12222", tmpDB.Name(), true, "")
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}
	defer server.Stop()

	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{"valid simple name", "mycontainer", true},
		{"valid with hyphen", "my-container", true},
		{"valid with numbers", "container123", true},
		{"valid mixed", "my-container-123", true},
		{"minimum length", "abc", true},
		{"maximum length", "abcdefghij1234567890", true},
		{"too short", "ab", false},
		{"too long", "toolongcontainernamehere123", false},
		{"uppercase", "MyContainer", false},
		{"underscore", "my_container", false},
		{"space", "my container", false},
		{"starts with hyphen", "-container", false},
		{"ends with hyphen", "container-", false},
		{"consecutive hyphens", "my--container", false},
		{"numbers only", "123", true},
		{"single char", "a", false},
		{"empty", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := server.isValidContainerName(tt.input)
			if result != tt.expected {
				t.Errorf("isValidContainerName(%q) = %v, want %v", tt.input, result, tt.expected)
			}
		})
	}
}

func TestContainerNamesSameAsTeamNames(t *testing.T) {
	// Create temporary database file
	tmpDB, err := os.CreateTemp("", "test_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp db: %v", err)
	}
	defer os.Remove(tmpDB.Name())
	tmpDB.Close()

	server, err := NewServer(":18080", "", ":12222", tmpDB.Name(), true, "")
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}
	defer server.Stop()

	// Test that container name validation uses same rules as team names
	testNames := []string{
		"validname",
		"valid-name",
		"name123", 
		"my-name-123",
		"ab",              // too short
		"toolongname12345", // too long
		"Name",            // uppercase
		"name_with_under", // underscore
		"name with space", // space
		"-name",           // starts with hyphen
		"name-",           // ends with hyphen
		"name--with",      // consecutive hyphens
	}

	for _, name := range testNames {
		containerValid := server.isValidContainerName(name)
		teamValid := server.isValidTeamName(name)
		
		if containerValid != teamValid {
			t.Errorf("Container name validation differs from team name validation for %q: container=%v, team=%v", 
				name, containerValid, teamValid)
		}
	}
}