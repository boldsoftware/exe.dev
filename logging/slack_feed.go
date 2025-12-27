package logging

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/go4org/hashtriemap"
	"github.com/slack-go/slack"
)

// SlackFeed manages posting events to the Slack #feed channel.
// All methods are non-blocking and best-effort.
type SlackFeed struct {
	client *slack.Client

	// Track new user signup messages for adding reactions when they create their first VM.
	// Maps userID -> message reference (channel + timestamp).
	// In-memory only, best effort.
	newUserMessages hashtriemap.HashTrieMap[string, slack.ItemRef]
}

// NewSlackFeed creates a new SlackFeed.
// If enabled is false or SLACK_BOT_TOKEN is not set, returns a SlackFeed
// that logs messages instead of posting to Slack.
func NewSlackFeed(enabled bool) *SlackFeed {
	sf := new(SlackFeed)
	token := strings.TrimSpace(os.Getenv("SLACK_BOT_TOKEN"))
	if enabled && token != "" {
		sf.client = slack.New(token)
	}
	return sf
}

// NewUser notifies Slack of a new user signup.
func (sf *SlackFeed) NewUser(ctx context.Context, userID, email, source string) {
	message := fmt.Sprintf("new user (%s): `%s`", source, email)
	if sf.client == nil {
		slog.InfoContext(ctx, "slack #feed", "message", message)
		return
	}
	go func() {
		channel, ts, err := sf.client.PostMessageContext(context.WithoutCancel(ctx), "feed", slack.MsgOptionText(message, true))
		if err != nil {
			slog.WarnContext(ctx, "failed to post to #feed", "error", err)
			return
		}
		sf.newUserMessages.Store(userID, slack.NewRefToMessage(channel, ts))
	}()
}

// CreatedVM notifies Slack that user userID has created a VM.
func (sf *SlackFeed) CreatedVM(ctx context.Context, userID string) {
	ref, ok := sf.newUserMessages.LoadAndDelete(userID)
	if !ok {
		return
	}
	if sf.client == nil {
		slog.InfoContext(ctx, "slack #feed reaction", "emoji", "hatching_chick", "userID", userID)
		return
	}
	go func() {
		err := sf.client.AddReactionContext(context.WithoutCancel(ctx), "hatching_chick", ref)
		if err != nil {
			slog.WarnContext(ctx, "failed to add reaction to #feed message", "error", err, "userID", userID)
		}
	}()
}

// ServerStarted notifies #page that the server has started.
func (sf *SlackFeed) ServerStarted(ctx context.Context, gitSHA string) {
	hostname, _ := os.Hostname()
	shaLink := fmt.Sprintf("<https://github.com/boldsoftware/exe/commit/%s|%s>", gitSHA, gitSHA)
	message := fmt.Sprintf("exed %s started on %s", shaLink, hostname)
	if sf.client == nil {
		slog.InfoContext(ctx, "slack #page", "message", message)
		return
	}
	go func() {
		_, _, err := sf.client.PostMessageContext(context.WithoutCancel(ctx), "page", slack.MsgOptionText(message, false))
		if err != nil {
			slog.WarnContext(ctx, "failed to post to #page", "error", err)
		}
	}()
}
