package notifications

import "context"

// Channel is a backend notification delivery mechanism.
// Discord webhooks, email, etc. implement this interface.
type Channel interface {
	// Name returns the unique identifier for this channel.
	Name() string

	// Send delivers a notification event through this channel.
	Send(ctx context.Context, event Event) error
}
