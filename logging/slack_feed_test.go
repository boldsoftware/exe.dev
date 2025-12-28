package logging

import (
	"context"
	"testing"

	"github.com/slack-go/slack"
	"github.com/stretchr/testify/require"
)

func TestSlackFeed_NoClient(t *testing.T) {
	// When client is nil (disabled), methods should log but not panic
	sf := &SlackFeed{client: nil}

	ctx := context.Background()
	sf.NewUser(ctx, "user123", "test@example.com", "ssh")
	// NewUser with nil client doesn't store, so CreatedVM has nothing to find
	sf.CreatedVM(ctx, "user123")
}

func TestSlackFeed_TracksNewUserMessage(t *testing.T) {
	// Test that we can store and retrieve message refs
	sf := &SlackFeed{client: nil}

	// Manually store a message ref (simulating what NewUser does with a real client)
	ref := slack.NewRefToMessage("C123", "1234567890.123456")
	sf.newUserMessages.Store("user123", ref)

	// Verify it's stored
	val, ok := sf.newUserMessages.Load("user123")
	require.True(t, ok, "message ref should be stored")
	require.Equal(t, ref, val)
}

func TestSlackFeed_EmailVerified_NoOpIfNotFound(t *testing.T) {
	// Test that EmailVerified is a no-op if there's no stored message
	sf := &SlackFeed{client: nil}

	// Should not panic when there's no stored message
	sf.EmailVerified(context.Background(), "nonexistent")
}

func TestSlackFeed_CreatedVM_NoOpIfNotFound(t *testing.T) {
	// Test that CreatedVM is a no-op if there's no stored message
	sf := &SlackFeed{client: nil}

	// Should not panic or log when there's no stored message
	sf.CreatedVM(context.Background(), "nonexistent")
}

func TestSlackFeed_NewSlackFeed_NoToken(t *testing.T) {
	// When SLACK_BOT_TOKEN is not set, client should be nil
	t.Setenv("SLACK_BOT_TOKEN", "")
	sf := NewSlackFeed(true)
	require.Nil(t, sf.client, "client should be nil when no token")
}

func TestSlackFeed_NewSlackFeed_Disabled(t *testing.T) {
	// When disabled, client should be nil even if token is set
	t.Setenv("SLACK_BOT_TOKEN", "xoxb-fake-token")
	sf := NewSlackFeed(false)
	require.Nil(t, sf.client, "client should be nil when disabled")
}
