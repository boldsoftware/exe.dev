package execore

import (
	"context"
	"log/slog"
	"strings"
	"unicode/utf8"

	"exe.dev/execore/emojidata"
	"exe.dev/exedb"
	"exe.dev/exemenu"
	"github.com/rivo/uniseg"
)

// maxEmojiBytes bounds the emoji column to a small value since it is meant
// to hold a single emoji glyph (which can be multiple runes due to ZWJ
// sequences and variation selectors, but still well under 64 bytes).
const maxEmojiBytes = 64

// looksLikeEmoji reports whether s is a single grapheme cluster that contains
// at least one rune from the emoji-ish Unicode blocks. This is a pragmatic
// check meant to reject plain ASCII / text ("hi", "x") while accepting the
// usual single-emoji inputs including ZWJ sequences, variation selectors,
// skin tones, and flags. It is not a full Emoji spec implementation.
func looksLikeEmoji(s string) bool {
	if s == "" || !utf8.ValidString(s) {
		return false
	}
	if uniseg.GraphemeClusterCount(s) != 1 {
		return false
	}
	for _, r := range s {
		if isEmojiRune(r) {
			return true
		}
	}
	return false
}

func isEmojiRune(r rune) bool {
	switch {
	case r >= 0x1F000 && r <= 0x1FFFF: // pictographs, faces, food, flags, etc.
		return true
	case r >= 0x2600 && r <= 0x26FF: // misc symbols (☀-⛿)
		return true
	case r >= 0x2700 && r <= 0x27BF: // dingbats
		return true
	case r >= 0x2300 && r <= 0x23FF: // misc technical (⌚ watch, ⏰ clock, ⌨ keyboard...)
		return true
	case r >= 0x2B00 && r <= 0x2BFF: // misc symbols and arrows (⭐ star, ⬛ black square)
		return true
	case r == 0x203C || r == 0x2049 || r == 0x2122 || r == 0x2139:
		return true // !!, !?, ™, ℹ
	case r >= 0x2190 && r <= 0x21FF: // arrows
		return true
	case r >= 0x2460 && r <= 0x24FF: // enclosed alphanumerics (Ⓜ circled M)
		return true
	case r >= 0x25A0 && r <= 0x25FF: // geometric shapes
		return true
	}
	return false
}

func (ss *SSHServer) handleEmojiCommand(ctx context.Context, cc *exemenu.CommandContext) error {
	if len(cc.Args) != 2 {
		return cc.Errorf("usage: emoji <hostname> <emoji>")
	}

	vmName := ss.normalizeBoxName(cc.Args[0])
	raw := strings.TrimSpace(cc.Args[1])

	// Accept either a raw emoji glyph or a ":shortcode:" form (like :duck:)
	// understood by the GitHub emoji database.
	emoji, ok := emojidata.Resolve(raw)
	if !ok {
		return cc.Errorf("unknown emoji shortcode %q", raw)
	}

	if len(emoji) > maxEmojiBytes {
		return cc.Errorf("emoji too long (max %d bytes)", maxEmojiBytes)
	}
	if !looksLikeEmoji(emoji) {
		return cc.Errorf("%q does not look like a single emoji", raw)
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
	CommandLogAddAttr(ctx, slog.String("emoji", emoji))

	if err := withTx1(ss.server, ctx, (*exedb.Queries).SetBoxEmoji, exedb.SetBoxEmojiParams{
		Emoji: emoji,
		ID:    box.ID,
	}); err != nil {
		return cc.Errorf("failed to set emoji: %v", err)
	}

	if cc.WantJSON() {
		cc.WriteJSON(map[string]any{
			"vm_name": vmName,
			"emoji":   emoji,
		})
		return nil
	}

	cc.Writeln("Set emoji for %q to %s", vmName, emoji)
	return nil
}

// completeEmojiArgs completes the `emoji` command: box names for arg 1,
// `:shortcode:` entries from the GitHub emoji database for arg 2.
func (ss *SSHServer) completeEmojiArgs(compCtx *exemenu.CompletionContext, cc *exemenu.CommandContext) []string {
	if compCtx.Position <= 1 {
		return ss.completeBoxNames(compCtx, cc)
	}
	if compCtx.Position != 2 {
		return nil
	}
	prefix := compCtx.CurrentWord
	// Only offer shortcode completions once the user has typed a leading colon.
	if !strings.HasPrefix(prefix, ":") {
		return nil
	}
	name := strings.TrimPrefix(prefix, ":")
	name = strings.TrimSuffix(name, ":")
	var out []string
	for _, e := range emojidata.Entries() {
		if strings.HasPrefix(e.ShortName, name) {
			out = append(out, ":"+e.ShortName+":")
			if len(out) >= 50 {
				break
			}
		}
	}
	return out
}
