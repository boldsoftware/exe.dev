package server

import (
	"strings"
	"testing"
)

// TestWorkingDirectoryConfiguration tests that the working directory (cwd) setting
// is properly passed through from HTTP requests to tool execution.
func TestWorkingDirectoryConfiguration(t *testing.T) {
	h := NewTestHarness(t)
	defer h.Close()

	t.Run("cwd_tmp", func(t *testing.T) {
		h.NewConversation("bash: pwd", "/tmp")
		result := strings.TrimSpace(h.WaitToolResult())
		if result != "/tmp" {
			t.Errorf("expected '/tmp', got: %s", result)
		}
	})

	t.Run("cwd_root", func(t *testing.T) {
		h.NewConversation("bash: pwd", "/")
		result := strings.TrimSpace(h.WaitToolResult())
		if result != "/" {
			t.Errorf("expected '/', got: %s", result)
		}
	})
}
