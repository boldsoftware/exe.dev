package execore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"exe.dev/exedb"
	"exe.dev/exemenu"
	"exe.dev/sshkey"
	"github.com/anmitsu/go-shlex"
	"golang.org/x/crypto/ssh"
)

// DefaultTokenCmds is the list of commands allowed when a token does not specify cmds.
var DefaultTokenCmds = []string{"help", "ls", "new", "whoami", "ssh-key list", "share show"}

// TokenResult contains the result of a successful token validation.
type TokenResult struct {
	UserID  string         // The user ID associated with the signing key.
	Payload map[string]any // The validated JSON payload from the token (parsed).
	CtxRaw  []byte         // The raw bytes of the "ctx" field (for verbatim passthrough to VMs).
	Cmds    []string       // Allowed commands (nil means use DefaultTokenCmds).
}

// validateToken validates an SSH-signed token and returns the user ID and payload.
// The namespace parameter specifies the expected signing namespace (e.g., "v0@" + env.WebHost).
// Returns an error if the token is invalid, expired, or the signature doesn't verify.
func (s *Server) validateToken(ctx context.Context, token, namespace string) (*TokenResult, error) {
	// Parse and validate token format
	parsed, err := sshkey.ParseToken(token)
	if err != nil {
		return nil, err
	}

	// Validate exp and nbf claims
	if err := parsed.ValidateClaims(); err != nil {
		return nil, err
	}

	// Look up the SSH key by fingerprint (database access stays here).
	// Auth-sensitive errors use a generic message to avoid leaking
	// whether a fingerprint exists or whether the signature is wrong.
	keyRow, err := s.getSSHKeyByFingerprint(ctx, parsed.Fingerprint)
	if err != nil {
		return nil, errors.New("invalid token")
	}

	// Parse the public key
	matchedKey, _, _, _, err := ssh.ParseAuthorizedKey([]byte(keyRow.PublicKey))
	if err != nil {
		s.slog().ErrorContext(ctx, "failed to parse stored SSH key", "error", err, "fingerprint", parsed.Fingerprint)
		return nil, errors.New("invalid token")
	}

	// Verify the SSH signature
	if err := parsed.Verify(matchedKey, namespace); err != nil {
		s.slog().WarnContext(ctx, "SSH signature verification failed", "error", err, "fingerprint", parsed.Fingerprint, "namespace", namespace)
		return nil, errors.New("invalid token")
	}

	// Check if the key owner is locked out (fail closed on DB error).
	isLockedOut, err := s.isUserLockedOut(ctx, keyRow.UserID)
	if err != nil {
		s.slog().ErrorContext(ctx, "failed to check lockout status", "error", err, "user_id", keyRow.UserID)
		return nil, errors.New("invalid token")
	}
	if isLockedOut {
		s.slog().WarnContext(ctx, "locked out user attempted token auth", "user_id", keyRow.UserID, "fingerprint", parsed.Fingerprint)
		return nil, errors.New("invalid token")
	}

	return &TokenResult{
		UserID:  keyRow.UserID,
		Payload: parsed.PayloadJSON,
		CtxRaw:  parsed.CtxRaw,
		Cmds:    parsed.Cmds,
	}, nil
}

// execJSONError writes a JSON error response with the given status code and message.
func execJSONError(w http.ResponseWriter, message string, code int) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}

// handleExec handles POST /exec requests with SSH signature authentication.
func (s *Server) handleExec(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		execJSONError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Extract Bearer token from Authorization header.
	// RFC 7235: auth scheme is case-insensitive.
	auth := r.Header.Get("Authorization")
	const bearerPrefix = "Bearer "
	if len(auth) < len(bearerPrefix) || !strings.EqualFold(auth[:len(bearerPrefix)], bearerPrefix) {
		w.Header().Set("WWW-Authenticate", "Bearer")
		execJSONError(w, "missing or invalid Authorization header", http.StatusUnauthorized)
		return
	}
	token := auth[len(bearerPrefix):]

	// Validate the token.
	ctx := r.Context()
	result, err := s.validateToken(ctx, token, "v0@"+s.env.WebHost)
	if err != nil {
		w.Header().Set("WWW-Authenticate", "Bearer")
		execJSONError(w, err.Error(), http.StatusUnauthorized)
		return
	}

	// Get user details for CommandContext.
	user, err := withRxRes1(s, ctx, (*exedb.Queries).GetUserWithDetails, result.UserID)
	if err != nil {
		s.slog().ErrorContext(ctx, "failed to get user details", "error", err, "user_id", result.UserID)
		execJSONError(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Read the command from the request body.
	// The documented limit is 64KB; read one extra byte to detect overflow.
	const maxBodySize = 64*1024 + 1
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodySize))
	if err != nil {
		execJSONError(w, "failed to read request body", http.StatusBadRequest)
		return
	}
	if len(body) >= maxBodySize {
		execJSONError(w, "request body exceeds 64KB limit", http.StatusRequestEntityTooLarge)
		return
	}
	if bytes.ContainsRune(body, 0) {
		execJSONError(w, "request body must not contain null bytes", http.StatusBadRequest)
		return
	}
	cmd := strings.TrimSpace(string(body))
	if cmd == "" {
		execJSONError(w, "missing command in request body", http.StatusBadRequest)
		return
	}

	// Parse command using shell lexer (same as SSH REPL).
	cmdParts, err := shlex.Split(cmd, true)
	if err != nil {
		execJSONError(w, "invalid command syntax", http.StatusBadRequest)
		return
	}
	if len(cmdParts) == 0 {
		execJSONError(w, "missing command in request body", http.StatusBadRequest)
		return
	}

	// Create SSH server and command context.
	ss := NewSSHServer(s)

	// Resolve the command name using the command tree, then enforce token cmds.
	resolvedCmd := ss.commands.ResolveCommandName(cmdParts)
	if resolvedCmd == "" {
		execJSONError(w, "unknown command", http.StatusNotFound)
		return
	}
	if !tokenCmdsAllow(result.Cmds, resolvedCmd) {
		execJSONError(w, "command not allowed by token permissions", http.StatusForbidden)
		return
	}

	var outputBuf bytes.Buffer
	cc := &exemenu.CommandContext{
		User:      &exemenu.UserInfo{ID: result.UserID, Email: user.Email},
		Output:    exemenu.NewANSIFilterWriter(&outputBuf),
		Logger:    s.slog(),
		ForceJSON: true,
	}

	// Execute the command with timeout.
	execCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	exitCode := ss.commands.ExecuteCommand(execCtx, cc, cmdParts)

	output := strings.TrimSpace(outputBuf.String())

	// Check for timeout only on failure — a successful command that happens
	// to finish right at the deadline should still return its output.
	if exitCode != 0 && execCtx.Err() == context.DeadlineExceeded {
		execJSONError(w, "command timed out", http.StatusGatewayTimeout)
		return
	}

	// Return output.
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if exitCode != 0 {
		w.WriteHeader(http.StatusUnprocessableEntity)
	}
	w.Write([]byte(output))
	if len(output) > 0 && output[len(output)-1] != '\n' {
		w.Write([]byte("\n"))
	}
}

// tokenCmdsAllow checks whether resolvedCmd is allowed by the token's cmds list.
// If cmds is nil, DefaultTokenCmds is used. An empty (non-nil) cmds list blocks all commands.
// Matching is exact: the resolved command name (e.g., "ssh-key list") must appear
// in the cmds list. Including "ssh-key" does NOT grant access to "ssh-key list".
func tokenCmdsAllow(cmds []string, resolvedCmd string) bool {
	if cmds == nil {
		cmds = DefaultTokenCmds
	}
	if resolvedCmd == "" {
		return false
	}
	return slices.Contains(cmds, resolvedCmd)
}

// getSSHKeyByFingerprint looks up an SSH key by its fingerprint.
func (s *Server) getSSHKeyByFingerprint(ctx context.Context, fingerprint string) (exedb.GetSSHKeyByFingerprintRow, error) {
	return withRxRes1(s, ctx, (*exedb.Queries).GetSSHKeyByFingerprint, fingerprint)
}
