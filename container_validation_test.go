package exe

import (
	"os"
	"testing"
)

func TestIsValidMachineName(t *testing.T) {
	t.Parallel()
	// Create temporary database file
	tmpDB, err := os.CreateTemp("", "test_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp db: %v", err)
	}
	defer os.Remove(tmpDB.Name())
	tmpDB.Close()

	server, err := NewServer(":18080", "", ":12222", ":0", tmpDB.Name(), "local", []string{""})
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}

	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		// Valid names
		{"valid simple 5 chars", "myapp", true},
		{"valid with numbers", "web123", true},
		{"valid with hyphens", "my-app", true},
		{"valid long name", "very-long-machine-name-twelve", true},
		{"valid 32 chars", "abcdefghijklmnopqrstuvwxyz123456", true},
		{"valid 5 chars exactly", "hello", true},
		{"numbers at end", "app123", true},
		{"valid longer name", "mymachine", true},

		// Invalid names - too short (less than 5 chars)
		{"single letter", "a", false},
		{"two chars", "ab", false},
		{"three chars", "abc", false},
		{"four chars", "abcd", false},

		// Invalid names - denylist
		{"denylisted debug", "debug", false},
		{"denylisted admin", "admin", false},
		{"denylisted shell", "shell", false},
		{"denylisted class", "class", false},
		{"denylisted array", "array", false},
		{"denylisted index", "index", false},
		{"denylisted login", "login", false},
		{"denylisted proxy", "proxy", false},
		{"denylisted cache", "cache", false},
		{"denylisted error", "error", false},

		// Invalid names - other format issues
		{"empty string", "", false},
		{"too long", "abcdefghijklmnopqrstuvwxyz1234567", false}, // 33 chars
		{"starts with number", "123app", false},
		{"starts with hyphen", "-myapp", false},
		{"ends with hyphen", "myapp-", false},
		{"consecutive hyphens", "my--app", false},
		{"contains uppercase", "MyApp", false},
		{"contains underscore", "my_app", false},
		{"contains space", "my app", false},
		{"contains special chars", "app@123", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := server.isValidMachineName(tt.input)
			if result != tt.expected {
				t.Errorf("isValidMachineName(%q) = %v, want %v", tt.input, result, tt.expected)
			}
		})
	}
}

func TestMachineNameDenylist(t *testing.T) {
	t.Parallel()
	// Create temporary database file
	tmpDB, err := os.CreateTemp("", "test_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp db: %v", err)
	}
	defer os.Remove(tmpDB.Name())
	tmpDB.Close()

	server, err := NewServer(":18080", "", ":12222", ":0", tmpDB.Name(), "local", []string{""})
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}

	// Test all denylisted machine names
	denylistedWords := []string{
		"abort", "admin", "allow", "array", "async", "audit", "block", "board", "boost", "break",
		"build", "bytes", "cable", "cache", "catch", "chain", "check", "chips", "class", "clock",
		"cloud", "codec", "codes", "const", "cores", "crawl", "crypt", "debug", "drive", "email",
		"entry", "error", "event", "fetch", "fiber", "field", "flash", "frame", "games", "grant",
		"guard", "guest", "https", "image", "index", "input", "laser", "links", "logic", "login",
		"macro", "match", "merge", "modem", "mount", "nodes", "parse", "paste", "patch", "pixel",
		"ports", "power", "print", "proxy", "query", "radio", "regex", "reset", "route", "scope",
		"serve", "setup", "share", "shell", "solid", "sound", "speed", "spell", "stack", "start",
		"store", "style", "table", "theme", "throw", "timer", "token", "tower", "trace", "trash",
		"trust", "users", "video", "virus", "watts",
	}

	for _, word := range denylistedWords {
		t.Run("denylisted word: "+word, func(t *testing.T) {
			result := server.isValidMachineName(word)
			if result {
				t.Errorf("Expected denylisted word %q to be invalid, but it was accepted", word)
			}
		})
	}

	// Test that similar but not exactly matching words are still valid
	validSimilar := []string{
		"debugging", "admins", "allows", "arrays", "asyncs",
		"blocks2", "builds", "caches", "errors", "logins",
		"proxys", "shells", "stacks", "tokens", "videos",
	}

	for _, word := range validSimilar {
		t.Run("valid similar word: "+word, func(t *testing.T) {
			result := server.isValidMachineName(word)
			if !result {
				t.Errorf("Expected word %q to be valid (similar to denylist but not exact match), but it was rejected", word)
			}
		})
	}
}
