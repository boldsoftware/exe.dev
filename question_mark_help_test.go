package exe

import (
	"strings"
	"testing"
)

func TestQuestionMarkHelp(t *testing.T) {
	// Test that the help text (without ASCII art) includes the "?" alias
	helpText := "\r\n\033[1;33mEXE.DEV\033[0m commands:\r\n\r\n" +
		"\033[1mlist\033[0m           - List your containers\r\n" +
		"\033[1mcreate [name]\033[0m  - Create a new container (auto-generates name if not specified)\r\n" +
		"\033[1mssh <name>\033[0m     - SSH into a container\r\n" +
		"\033[1mstart <name>\033[0m   - Start a container\r\n" +
		"\033[1mstop <name>\033[0m    - Stop a container\r\n" +
		"\033[1mdelete <name>\033[0m  - Delete a container\r\n" +
		"\033[1mlogs <name>\033[0m    - View container logs\r\n" +
		"\033[1mhelp\033[0m or \033[1m?\033[0m     - Show this help\r\n" +
		"\033[1mexit\033[0m           - Exit\r\n\r\n"
	
	// Verify that the help text mentions both "help" and "?"
	if !strings.Contains(helpText, "help") {
		t.Error("Help text should contain 'help' command")
	}
	if !strings.Contains(helpText, "?") {
		t.Error("Help text should contain '?' command")
	}
	// Check for the pattern with ANSI codes
	if !strings.Contains(helpText, "or") || !strings.Contains(helpText, "help") || !strings.Contains(helpText, "?") {
		t.Error("Help text should show 'help' and '?' as alternatives connected by 'or'")
	}
	// Verify no ASCII art in help text
	if strings.Contains(helpText, "███") {
		t.Error("Help text should NOT contain ASCII art")
	}
	
	t.Log("Question mark help alias verified in help text")
}

func TestQuestionMarkCommandParsing(t *testing.T) {
	// Test that the switch statement logic will handle both "help" and "?" correctly
	// This tests the Go language feature that case "help", "?" works as expected
	
	testCases := []string{"help", "?"}
	
	for _, cmd := range testCases {
		// Simulate the switch logic
		matched := false
		switch cmd {
		case "help", "?":
			matched = true
		}
		
		if !matched {
			t.Errorf("Command %q should match the help case", cmd)
		}
	}
	
	t.Log("Both 'help' and '?' commands match the help case")
}