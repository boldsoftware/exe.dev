package execore

import (
	"context"
	"log/slog"
	"strings"
	"unicode/utf8"

	"exe.dev/exedb"
	"exe.dev/exemenu"
)

// maxEmojiBytes bounds the emoji column to a small value since it is meant
// to hold a single emoji glyph (which can be multiple runes due to ZWJ
// sequences and variation selectors, but still well under 64 bytes).
const maxEmojiBytes = 64

func (ss *SSHServer) handleEmojiCommand(ctx context.Context, cc *exemenu.CommandContext) error {
	if len(cc.Args) != 2 {
		return cc.Errorf("usage: emoji <hostname> <emoji>")
	}

	vmName := ss.normalizeBoxName(cc.Args[0])
	emoji := strings.TrimSpace(cc.Args[1])

	if len(emoji) > maxEmojiBytes {
		return cc.Errorf("emoji too long (max %d bytes)", maxEmojiBytes)
	}
	if strings.ContainsAny(emoji, "\n\r\t") {
		return cc.Errorf("emoji must not contain whitespace control characters")
	}
	if emoji != "" && !utf8.ValidString(emoji) {
		return cc.Errorf("emoji is not valid UTF-8")
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

	if emoji == "" {
		cc.Writeln("Cleared emoji for %q", vmName)
	} else {
		cc.Writeln("Set emoji for %q to %s", vmName, emoji)
	}
	return nil
}
