package logging

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"runtime/debug"
	"strings"
	"time"

	"exe.dev/stage"
	"github.com/go4org/hashtriemap"
	"github.com/slack-go/slack"
)

// SlackFeed manages posting events to Slack channels.
// All methods are non-blocking and best-effort.
type SlackFeed struct {
	client *slack.Client
	log    *slog.Logger
	env    stage.Env

	// Track new user signup messages for adding reactions when they create their first VM.
	// Maps userID -> message reference (channel + timestamp).
	// In-memory only, best effort.
	newUserMessages hashtriemap.HashTrieMap[string, slack.ItemRef]
}

// NewSlackFeed creates a new SlackFeed.
// If env.PostSlackFeed is false or SLACK_BOT_TOKEN is not set, returns a SlackFeed
// that logs messages instead of posting to Slack.
func NewSlackFeed(log *slog.Logger, env stage.Env) *SlackFeed {
	sf := &SlackFeed{
		log: log,
		env: env,
	}
	token := strings.TrimSpace(os.Getenv("SLACK_BOT_TOKEN"))
	if env.PostSlackFeed && token != "" {
		sf.client = slack.New(token)
	}
	return sf
}

// NewUser notifies Slack of a new user signup.
// If inviterEmail is non-empty, includes it in the message (for invite link signups).
func (sf *SlackFeed) NewUser(ctx context.Context, userID, email, source, inviterEmail string) {
	message := fmt.Sprintf("new user (%s): `%s`", source, email)
	if inviterEmail != "" {
		message += fmt.Sprintf(" (invited by `%s`)", inviterEmail)
	}
	if sf.client == nil {
		sf.log.InfoContext(ctx, "slack feed channel", "message", message)
		return
	}
	go func() {
		channel, ts, err := sf.client.PostMessageContext(context.WithoutCancel(ctx), sf.env.SlackFeedChannel, slack.MsgOptionText(message, true))
		if err != nil {
			sf.log.WarnContext(ctx, "failed to post to feed channel", "error", err)
			return
		}
		sf.newUserMessages.Store(userID, slack.NewRefToMessage(channel, ts))
	}()
}

// loadUserMessage loads the message for userID from sf.newUserMessages, retrying as needed.
// The retry is a cheap workaround for the fact that all our Slack work is non-blocking and async.
func (sf *SlackFeed) loadUserMessage(userID string) (slack.ItemRef, bool) {
	backoff := []time.Duration{
		0,
		200 * time.Millisecond, 500 * time.Millisecond,
		1 * time.Second, 2 * time.Second,
	}
	for _, delay := range backoff {
		time.Sleep(delay)
		if ref, ok := sf.newUserMessages.Load(userID); ok {
			return ref, true
		}
	}
	return slack.ItemRef{}, false
}

// EmailVerified notifies Slack that user userID has verified their email.
func (sf *SlackFeed) EmailVerified(ctx context.Context, userID string) {
	go func() {
		ref, ok := sf.loadUserMessage(userID)
		if !ok {
			return
		}
		if sf.client == nil {
			sf.log.InfoContext(ctx, "slack feed channel reaction", "emoji", "passport_control", "userID", userID)
			return
		}
		err := sf.client.AddReactionContext(context.WithoutCancel(ctx), "passport_control", ref)
		if err != nil {
			sf.log.WarnContext(ctx, "failed to add reaction to feed channel message", "error", err, "userID", userID)
		}
	}()
}

// TrialStarted notifies Slack that user userID started a trial.
func (sf *SlackFeed) TrialStarted(ctx context.Context, userID string) {
	go func() {
		ref, ok := sf.loadUserMessage(userID)
		if !ok {
			return
		}
		if sf.client == nil {
			sf.log.InfoContext(ctx, "slack feed channel reaction", "emoji", "hourglass_flowing_sand", "userID", userID)
			return
		}
		err := sf.client.AddReactionContext(context.WithoutCancel(ctx), "hourglass_flowing_sand", ref)
		if err != nil {
			sf.log.WarnContext(ctx, "failed to add reaction to feed channel message", "error", err, "userID", userID)
		}
	}()
}

// CreatedVM notifies Slack that user userID has created a VM.
func (sf *SlackFeed) CreatedVM(ctx context.Context, userID string) {
	go func() {
		ref, ok := sf.loadUserMessage(userID)
		if !ok {
			return
		}
		if sf.client == nil {
			sf.log.InfoContext(ctx, "slack feed channel reaction", "emoji", "hatching_chick", "userID", userID)
			return
		}
		err := sf.client.AddReactionContext(context.WithoutCancel(ctx), "hatching_chick", ref)
		if err != nil {
			sf.log.WarnContext(ctx, "failed to add reaction to feed channel message", "error", err, "userID", userID)
		}
	}()
}

// Subscribed notifies Slack that user userID has completed the Stripe subscription flow.
func (sf *SlackFeed) Subscribed(ctx context.Context, userID string) {
	go func() {
		ref, ok := sf.loadUserMessage(userID)
		if !ok {
			return
		}
		if sf.client == nil {
			sf.log.InfoContext(ctx, "slack feed channel reaction", "emoji", "money_with_wings", "userID", userID)
			return
		}
		err := sf.client.AddReactionContext(context.WithoutCancel(ctx), "money_with_wings", ref)
		if err != nil {
			sf.log.WarnContext(ctx, "failed to add reaction to feed channel message", "error", err, "userID", userID)
		}
	}()
}

// ServiceStarted notifies the ops and error channels that a service has started.
func (sf *SlackFeed) ServiceStarted(ctx context.Context, serviceName string) {
	hostname, _ := os.Hostname()
	sha := GitCommit()
	shaLink := fmt.Sprintf("<https://github.com/boldsoftware/exe/commit/%s|%s>", sha, sha)
	message := fmt.Sprintf("%s %s started on %s", serviceName, shaLink, hostname)
	if sf.client == nil {
		sf.log.InfoContext(ctx, "slack ops channel", "message", message)
		return
	}
	go func() {
		_, _, err := sf.client.PostMessageContext(context.WithoutCancel(ctx), sf.env.SlackOpsChannel, slack.MsgOptionText(message, false))
		if err != nil {
			sf.log.WarnContext(ctx, "failed to post to ops channel", "error", err)
		}
	}()
	if sf.env.LogErrorSlackChannel != "" {
		go func() {
			_, _, err := sf.client.PostMessageContext(context.WithoutCancel(ctx), sf.env.LogErrorSlackChannel, slack.MsgOptionText(message, false))
			if err != nil {
				sf.log.WarnContext(ctx, "failed to post to error channel", "error", err)
			}
		}()
	}
}

// GitCommit extracts the git SHA from build info.
func GitCommit() string {
	bi, _ := debug.ReadBuildInfo()
	if bi != nil {
		for _, setting := range bi.Settings {
			if setting.Key == "vcs.revision" {
				return setting.Value
			}
		}
	}
	return "unknown"
}

// PreferredExeletChanged notifies the ops channel when the preferred exelet is set or cleared.
func (sf *SlackFeed) PreferredExeletChanged(ctx context.Context, address string) {
	message := "preferred exelet cleared"
	if address != "" {
		message = fmt.Sprintf("preferred exelet set to `%s`", address)
	}
	if sf.client == nil {
		sf.log.InfoContext(ctx, "slack ops channel", "message", message)
		return
	}
	go func() {
		_, _, err := sf.client.PostMessageContext(context.WithoutCancel(ctx), sf.env.SlackOpsChannel, slack.MsgOptionText(message, false))
		if err != nil {
			sf.log.WarnContext(ctx, "failed to post to ops channel", "error", err)
		}
	}()
}

// InviteRequest notifies Slack that a user requested more invite codes.
func (sf *SlackFeed) InviteRequest(ctx context.Context, email string, hasBilling bool) {
	billingStatus := "no billing"
	if hasBilling {
		billingStatus = "💸"
	}
	message := fmt.Sprintf("invite request: `%s` (%s) wants more invites", email, billingStatus)
	if sf.client == nil {
		sf.log.InfoContext(ctx, "slack feed channel", "message", message)
		return
	}
	go func() {
		_, _, err := sf.client.PostMessageContext(context.WithoutCancel(ctx), sf.env.SlackFeedChannel, slack.MsgOptionText(message, true))
		if err != nil {
			sf.log.WarnContext(ctx, "failed to post invite request to feed channel", "error", err)
		}
	}()
}

// IdeaSubmitted notifies Slack that a user submitted a new idea template.
func (sf *SlackFeed) IdeaSubmitted(ctx context.Context, email, title, slug string) {
	message := fmt.Sprintf("new idea submitted: *%s* (`%s`) by `%s`", title, slug, email)
	if sf.client == nil {
		sf.log.InfoContext(ctx, "slack feed channel", "message", message)
		return
	}
	go func() {
		_, _, err := sf.client.PostMessageContext(context.WithoutCancel(ctx), sf.env.SlackFeedChannel, slack.MsgOptionText(message, false))
		if err != nil {
			sf.log.WarnContext(ctx, "failed to post idea submission to feed channel", "error", err)
		}
	}()
}

// CreditGifted notifies Slack that credits were gifted to a user.
func (sf *SlackFeed) CreditGifted(ctx context.Context, email string, amountUSD float64, note string) {
	message := fmt.Sprintf("new credit gift of $%.2f: `%s`", amountUSD, email)
	if note != "" {
		message += fmt.Sprintf(" — %s", note)
	}
	if sf.client == nil {
		sf.log.InfoContext(ctx, "slack feed channel", "message", message)
		return
	}
	go func() {
		channel, ts, err := sf.client.PostMessageContext(context.WithoutCancel(ctx), sf.env.SlackFeedChannel, slack.MsgOptionText(message, true))
		if err != nil {
			sf.log.WarnContext(ctx, "failed to post credit gift to feed channel", "error", err)
			return
		}
		ref := slack.NewRefToMessage(channel, ts)
		if err := sf.client.AddReactionContext(context.WithoutCancel(ctx), "gift", ref); err != nil {
			sf.log.WarnContext(ctx, "failed to add reaction to credit gift message", "error", err)
		}
	}()
}

// NewTeam notifies Slack that a new team was created.
func (sf *SlackFeed) NewTeam(ctx context.Context, billingOwnerEmail string) {
	message := fmt.Sprintf("new team: `%s`", billingOwnerEmail)
	if sf.client == nil {
		sf.log.InfoContext(ctx, "slack feed channel", "message", message)
		return
	}
	go func() {
		channel, ts, err := sf.client.PostMessageContext(context.WithoutCancel(ctx), sf.env.SlackFeedChannel, slack.MsgOptionText(message, true))
		if err != nil {
			sf.log.WarnContext(ctx, "failed to post new team to feed channel", "error", err)
			return
		}
		ref := slack.NewRefToMessage(channel, ts)
		if err := sf.client.AddReactionContext(context.WithoutCancel(ctx), "busts_in_silhouette", ref); err != nil {
			sf.log.WarnContext(ctx, "failed to add reaction to new team message", "error", err)
		}
	}()
}

// TeamDowngrade notifies Slack that a team was downgraded.
func (sf *SlackFeed) TeamDowngrade(ctx context.Context, billingOwnerEmail string) {
	message := fmt.Sprintf("team downgrade: `%s`", billingOwnerEmail)
	if sf.client == nil {
		sf.log.InfoContext(ctx, "slack feed channel", "message", message)
		return
	}
	go func() {
		channel, ts, err := sf.client.PostMessageContext(context.WithoutCancel(ctx), sf.env.SlackFeedChannel, slack.MsgOptionText(message, true))
		if err != nil {
			sf.log.WarnContext(ctx, "failed to post team downgrade to feed channel", "error", err)
			return
		}
		ref := slack.NewRefToMessage(channel, ts)
		if err := sf.client.AddReactionContext(context.WithoutCancel(ctx), "bust_in_silhouette", ref); err != nil {
			sf.log.WarnContext(ctx, "failed to add reaction to team downgrade message", "error", err)
		}
	}()
}

// ExeletCapacityWarning posts an urgent page that all exelets
// are approaching capacity.
func (sf *SlackFeed) ExeletCapacityWarning(ctx context.Context) {
	message := "all exelets approaching VM capacity"
	if sf.client == nil {
		sf.log.InfoContext(ctx, "slack page channel", "message", message)
		return
	}
	go func() {
		_, _, err := sf.client.PostMessageContext(context.WithoutCancel(ctx), sf.env.SlackPageChannel, slack.MsgOptionText(message, false))
		if err != nil {
			sf.log.WarnContext(ctx, "failed to post to page channel", "error", err)
		}
	}()
}
