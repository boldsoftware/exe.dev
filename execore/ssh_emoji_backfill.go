package execore

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"exe.dev/exedb"
	"exe.dev/exemenu"
)

// handleBackfillEmoji iterates over all VMs with an empty emoji column and
// asks Claude Haiku to produce a single, SFW, non-political emoji based on
// the VM's name.
func (ss *SSHServer) handleBackfillEmoji(ctx context.Context, cc *exemenu.CommandContext) error {
	if !ss.server.UserHasExeSudo(ctx, cc.User.ID) {
		return cc.Errorf("%s is not in the sudoers file. This incident will be reported.", cc.User.Email)
	}

	if len(cc.Args) != 1 {
		return cc.Errorf("usage: backfill-emoji <count>")
	}
	limit, err := strconv.ParseInt(cc.Args[0], 10, 64)
	if err != nil || limit <= 0 {
		return cc.Errorf("count must be a positive integer")
	}

	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		return cc.Errorf("ANTHROPIC_API_KEY not configured on server")
	}

	boxes, err := withRxRes1(ss.server, ctx, (*exedb.Queries).GetBoxesWithEmptyEmoji, limit)
	if err != nil {
		return cc.Errorf("failed to query boxes: %v", err)
	}

	if len(boxes) == 0 {
		cc.Writeln("No boxes with empty emoji found.")
		return nil
	}

	cc.Writeln("Found %d boxes to backfill.", len(boxes))

	httpc := &http.Client{Timeout: 30 * time.Second}
	var updated, failed int
	for _, b := range boxes {
		emoji, err := pickEmojiForName(ctx, httpc, apiKey, b.Name, nil)
		if err != nil {
			cc.Writeln("  %s: %v", b.Name, err)
			failed++
			continue
		}
		if err := withTx1(ss.server, ctx, (*exedb.Queries).SetBoxEmoji, exedb.SetBoxEmojiParams{
			Emoji: emoji,
			ID:    b.ID,
		}); err != nil {
			cc.Writeln("  %s: DB update failed: %v", b.Name, err)
			failed++
			continue
		}
		cc.Writeln("  %s: %s", b.Name, emoji)
		updated++
	}

	cc.Writeln("Done. updated=%d failed=%d", updated, failed)
	return nil
}

// pickEmojiForName asks Claude Haiku for a single safe-for-work, non-political
// emoji to associate with the given VM name. If avoid is non-empty, the model
// is instructed not to return any of those emojis (so a user's VMs end up with
// distinct emojis).
func pickEmojiForName(ctx context.Context, httpc *http.Client, apiKey, name string, avoid []string) (string, error) {
	const model = "claude-haiku-4-5-20251001"

	systemPrompt := "You pick a single emoji to represent a cloud VM based on its name. " +
		"Rules: respond with exactly one emoji character and nothing else. " +
		"The emoji must be safe for work and non-political. " +
		"Do not explain, do not quote, do not add punctuation or whitespace."
	userPrompt := fmt.Sprintf("VM name: %s\n\nReply with a single emoji.", name)
	if len(avoid) > 0 {
		userPrompt += "\n\nDo not use any of these emojis (already used for other VMs): " + strings.Join(avoid, " ")
	}

	reqBody, err := json.Marshal(map[string]any{
		"model":      model,
		"max_tokens": 32,
		"system":     systemPrompt,
		"messages": []map[string]any{
			{"role": "user", "content": userPrompt},
		},
	})
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	baseURL := os.Getenv("ANTHROPIC_BASE_URL")
	if baseURL == "" {
		baseURL = "https://api.anthropic.com"
	}
	req, err := http.NewRequestWithContext(ctx, "POST", baseURL+"/v1/messages", bytes.NewReader(reqBody))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := httpc.Do(req)
	if err != nil {
		return "", fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("anthropic API error (status %d): %s", resp.StatusCode, string(body))
	}

	var apiResp struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return "", fmt.Errorf("unmarshal response: %w", err)
	}

	var text strings.Builder
	for _, c := range apiResp.Content {
		if c.Type == "text" {
			text.WriteString(c.Text)
		}
	}
	emoji := strings.TrimSpace(text.String())
	if emoji == "" {
		return "", fmt.Errorf("empty response from model")
	}
	if !utf8.ValidString(emoji) {
		return "", fmt.Errorf("model returned invalid UTF-8")
	}
	if len(emoji) > maxEmojiBytes {
		return "", fmt.Errorf("model response too long: %q", emoji)
	}
	return emoji, nil
}
