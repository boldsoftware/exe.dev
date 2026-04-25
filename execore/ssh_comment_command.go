package execore

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"unicode"
	"unicode/utf8"

	"exe.dev/exedb"
	"exe.dev/exemenu"
)

// disallowedFormatRune reports whether r is an invisible / formatting rune
// that has no business in a short plain-text note. We block these as defense
// in depth so a comment can't spoof other UI elements via:
//   - bidi overrides (would let "x\u202Eevol" render as "x love")
//   - zero-width chars (homoglyph attacks, hidden separators)
//   - byte-order marks / soft hyphens / interlinear annotations
//
// Vue's mustache interpolation already escapes HTML, but bidi attacks survive
// HTML escaping; rejecting the runes outright is simpler than relying on every
// downstream surface (web list, ANSI ls output, emails, command logs) to
// neutralize them.
func disallowedFormatRune(r rune) bool {
	if r == 0xAD || r == 0xFEFF { // soft hyphen, BOM/ZWNBSP
		return true
	}
	return unicode.In(r, unicode.Cf)
}

// maxCommentBytes bounds the length of a VM comment. Comments are intended as
// short human-readable notes ("staging copy", "do not delete", etc.), not as
// long-form documentation.
const maxCommentBytes = 200

// validateComment normalizes and validates a user-supplied VM comment.
// It returns the cleaned comment or a user-facing error.
//
// Validation rules:
//   - Must be valid UTF-8.
//   - Length capped at maxCommentBytes after trimming.
//   - No control characters (other than the implicit empty-string clear).
//   - No HTML metacharacters that could enable injection in any view that
//     forgets to escape (we render via {{ }} in Vue, but be defensive).
func validateComment(raw string) (string, error) {
	c := strings.TrimSpace(raw)
	if c == "" {
		return "", nil
	}
	if !utf8.ValidString(c) {
		return "", fmt.Errorf("comment is not valid UTF-8")
	}
	if len(c) > maxCommentBytes {
		return "", fmt.Errorf("comment too long (max %d bytes)", maxCommentBytes)
	}
	for _, r := range c {
		// Allow common whitespace as a single space; reject other control runes.
		if r == '\t' || r == '\n' || r == '\r' {
			return "", fmt.Errorf("comment must not contain newlines or tabs")
		}
		if unicode.IsControl(r) {
			return "", fmt.Errorf("comment must not contain control characters")
		}
		if disallowedFormatRune(r) {
			return "", fmt.Errorf("comment must not contain bidi or zero-width formatting characters")
		}
	}
	if strings.ContainsAny(c, "<>&\"'`") {
		return "", fmt.Errorf("comment must not contain HTML metacharacters (< > & \" ' `)")
	}
	return c, nil
}

func (ss *SSHServer) handleCommentCommand(ctx context.Context, cc *exemenu.CommandContext) error {
	// Require at least the hostname plus one text arg. To clear a comment,
	// users must explicitly pass an empty quoted string (e.g. `comment my-vm ""`)
	// rather than just omitting the argument, so that a typo'd `comment my-vm`
	// can't accidentally wipe a useful note.
	if len(cc.Args) < 2 {
		return cc.Errorf("usage: comment <hostname> <text>")
	}

	vmName := ss.normalizeBoxName(cc.Args[0])

	// Treat all remaining args as the comment text, joined with single spaces.
	// This way "comment foo hello world" works without quoting, while quoted
	// arguments still work too.
	raw := strings.Join(cc.Args[1:], " ")

	comment, err := validateComment(raw)
	if err != nil {
		return cc.Errorf("%v", err)
	}

	CommandLogAddAttr(ctx, slog.String("vm_name", vmName))

	box, _, err := ss.server.FindAccessibleBox(ctx, cc.User.ID, vmName)
	if err != nil {
		return cc.Errorf("vm %q not found", vmName)
	}
	if err := enforceTagScope(ctx, box); err != nil {
		return cc.Errorf("%v", err)
	}

	CommandLogAddAttr(ctx, slog.Int("vm_id", box.ID))
	CommandLogAddAttr(ctx, slog.String("comment", comment))

	if err := withTx1(ss.server, ctx, (*exedb.Queries).SetBoxComment, exedb.SetBoxCommentParams{
		Comment: comment,
		ID:      box.ID,
	}); err != nil {
		return cc.Errorf("failed to set comment: %v", err)
	}

	if cc.WantJSON() {
		cc.WriteJSON(map[string]any{
			"vm_name": vmName,
			"comment": comment,
		})
		return nil
	}

	if comment == "" {
		cc.Writeln("Cleared comment for %q", vmName)
	} else {
		cc.Writeln("Set comment for %q to %q", vmName, comment)
	}
	return nil
}
