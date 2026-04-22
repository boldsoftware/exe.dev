package execore

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"exe.dev/boxname"
	"exe.dev/exedb"
)

// startEmojiResolution kicks off Haiku-based emoji selection for a new VM,
// avoiding emojis the user is already using on other VMs. The returned channel
// yields at most one (possibly empty) emoji string.
//
// If override is non-empty, it's returned immediately without consulting the
// model. The channel is buffered so the sender never blocks if the consumer
// moves on after a timeout.
func (ss *SSHServer) startEmojiResolution(ctx context.Context, userID, name, override string) <-chan string {
	ch := make(chan string, 1)
	if override != "" {
		ch <- override
		close(ch)
		return ch
	}

	// Snapshot emojis the user already uses so Haiku can avoid them.
	used, err := withRxRes1(ss.server, ctx, (*exedb.Queries).GetEmojisUsedByUser, userID)
	if err != nil {
		slog.WarnContext(ctx, "emoji: failed to list used emojis", "user", userID, "error", err)
		used = nil
	}
	usedSet := make(map[string]bool, len(used))
	for _, e := range used {
		usedSet[e] = true
	}

	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		// No Haiku available: fall back immediately (word-based then random).
		ch <- emojiFallback(name, usedSet)
		close(ch)
		return ch
	}

	// Run the network call in the background, shielded from the request ctx
	// so a client hang-up doesn't abort an emoji we might still want to store.
	go func() {
		defer close(ch)
		bgCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		httpc := &http.Client{Timeout: 30 * time.Second}
		emoji, err := pickEmojiForName(bgCtx, httpc, apiKey, name, used)
		if err != nil {
			slog.InfoContext(ctx, "emoji: haiku request failed; using fallback",
				"name", name, "error", err)
			ch <- emojiFallback(name, usedSet)
			return
		}
		ch <- emoji
	}()
	return ch
}

// emojiFallback returns an emoji picked from the word map (preferred) or the
// hardcoded random list, avoiding anything in used when possible.
func emojiFallback(name string, used map[string]bool) string {
	if e, ok := boxname.EmojiForName(name, used); ok {
		return e
	}
	return boxname.FallbackEmoji(used)
}

// applyResolvedEmoji takes the result the pipeline has produced *by the time
// it's called* (the VM is ready). If Haiku hasn't returned yet, we don't wait
// any extra time: we pick a fallback and move on.
func (ss *SSHServer) applyResolvedEmoji(ctx context.Context, boxID int, name string, ch <-chan string) {
	var emoji string
	select {
	case emoji = <-ch:
	default:
		emoji = emojiFallback(name, nil)
		slog.InfoContext(ctx, "emoji: haiku not ready by VM-ready deadline; using fallback",
			"vm_id", boxID, "name", name, "emoji", emoji)
	}
	emoji = strings.TrimSpace(emoji)
	if emoji == "" || !utf8.ValidString(emoji) || len(emoji) > maxEmojiBytes {
		emoji = boxname.FallbackEmoji(nil)
	}

	bgCtx := context.WithoutCancel(ctx)
	if err := withTx1(ss.server, bgCtx, (*exedb.Queries).SetBoxEmoji, exedb.SetBoxEmojiParams{
		Emoji: emoji,
		ID:    boxID,
	}); err != nil {
		slog.WarnContext(ctx, "emoji: failed to set on new VM",
			"vm_id", boxID, "emoji", emoji, "error", err)
	}
}
