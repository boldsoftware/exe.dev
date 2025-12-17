package logging

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/slack-go/slack"
)

var slackFeedClient *slack.Client

// InitSlackFeed initializes the Slack feed client.
// It must be called before PostFeedEvent.
// If enabled is false or SLACK_BOT_TOKEN is not set,
// PostFeedEvent will log messages instead of posting.
func InitSlackFeed(enabled bool) {
	token := strings.TrimSpace(os.Getenv("SLACK_BOT_TOKEN"))
	if !enabled || token == "" {
		return
	}
	slackFeedClient = slack.New(token)
}

// PostFeedEvent posts an event message to the #feed Slack channel.
// If slack feed was not initialized or disabled, logs the message instead.
// This function best-effort and non-blocking; it logs errors rather than returning them.
func PostFeedEvent(ctx context.Context, msg string, args ...any) {
	message := fmt.Sprintf(msg, args...)
	if slackFeedClient == nil {
		slog.InfoContext(ctx, "slack #feed", "message", message)
		return
	}
	go func() {
		_, _, err := slackFeedClient.PostMessageContext(context.WithoutCancel(ctx), "feed",
			slack.MsgOptionText(message, false),
		)
		if err != nil {
			slog.WarnContext(ctx, "failed to post to #feed", "error", err)
		}
	}()
}
