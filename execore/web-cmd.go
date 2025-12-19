package execore

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"exe.dev/exedb"
	"exe.dev/exemenu"
	"github.com/anmitsu/go-shlex"
)

// allowedCommands defines which commands can be run via the web UI.
// The key is the command prefix (first 1-2 words), value is true if allowed.
var allowedCommands = map[string]bool{
	"rm":                true,
	"share show":        true,
	"share port":        true,
	"share add":         true,
	"share remove":      true,
	"share add-link":    true,
	"share remove-link": true,
	"share set-public":  true,
	"share set-private": true,
}

// isCommandAllowed checks if a command is in the allowlist
func isCommandAllowed(cmd string) bool {
	cmd = strings.TrimSpace(cmd)
	parts := strings.Fields(cmd)
	if len(parts) == 0 {
		return false
	}

	// Check single-word commands
	if allowedCommands[parts[0]] {
		return true
	}

	// Check two-word commands (e.g., "share add")
	if len(parts) >= 2 {
		twoWord := parts[0] + " " + parts[1]
		if allowedCommands[twoWord] {
			return true
		}
	}

	return false
}

// handleRunCommand handles running SSH commands via the web UI
func (s *Server) handleRunCommand(w http.ResponseWriter, r *http.Request) {
	// Require authentication
	userID, err := s.validateAuthCookie(r)
	if err != nil {
		http.Error(w, "Authentication required", http.StatusUnauthorized)
		return
	}

	// Parse request
	var req struct {
		Command string `json:"command"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	cmd := strings.TrimSpace(req.Command)
	if cmd == "" {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"success": false,
			"error":   "Command is required",
		})
		return
	}

	// Validate command against allowlist
	if !isCommandAllowed(cmd) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"success": false,
			"error":   "Command not allowed",
		})
		return
	}

	// Get user details for CommandContext
	user, err := withRxRes1(s, r.Context(), (*exedb.Queries).GetUserWithDetails, userID)
	if err != nil {
		s.slog().ErrorContext(r.Context(), "Failed to get user details", "error", err, "user_id", userID)
		http.Error(w, "Failed to get user details", http.StatusInternalServerError)
		return
	}

	// Parse command using shell lexer to handle quotes properly
	cmdParts, err := shlex.Split(cmd, true)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"success": false,
			"error":   "Invalid command syntax",
		})
		return
	}

	// Create SSH server and command context
	ss := NewSSHServer(s)

	// Capture output
	var outputBuf bytes.Buffer

	cc := &exemenu.CommandContext{
		User:   &exemenu.UserInfo{ID: userID, Email: user.Email},
		Output: exemenu.NewANSIFilterWriter(&outputBuf),
		Logger: s.slog(),
	}

	// Execute the command with 10 second timeout
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	exitCode := ss.commands.ExecuteCommand(ctx, cc, cmdParts)

	// Prepare response
	w.Header().Set("Content-Type", "application/json")

	output := strings.TrimSpace(outputBuf.String())

	// Check for timeout
	if ctx.Err() == context.DeadlineExceeded {
		json.NewEncoder(w).Encode(map[string]any{
			"success": false,
			"error":   "Command timed out",
			"output":  output,
		})
		return
	}

	if exitCode != 0 {
		// Try to parse error from output, otherwise use generic message
		errMsg := output
		if errMsg == "" {
			errMsg = "Command failed"
		}
		json.NewEncoder(w).Encode(map[string]any{
			"success": false,
			"error":   errMsg,
			"output":  output,
		})
		return
	}

	json.NewEncoder(w).Encode(map[string]any{
		"success": true,
		"output":  output,
	})
}
