// Package bsdns (Box Shard DNS) provides a unified interface for DNS management across different providers.
package bsdns

import "context"

// Provider manages DNS records for boxes.
// It abstracts away the differences between providers (route53 creates CNAMEs,
// alley53 creates A records) behind a common shard-based interface.
type Provider interface {
	// UpsertBoxRecord creates or updates a DNS record for a box.
	UpsertBoxRecord(ctx context.Context, domain, boxName string, shard int) error

	// DeleteBoxRecord removes the DNS record for a box.
	DeleteBoxRecord(ctx context.Context, domain, boxName string, shard int) error
}

// Discard is a no-op DNS provider that silently succeeds without doing anything.
// Use this when no DNS provider is available (e.g., in tests without alley53).
type Discard struct{}

func (Discard) UpsertBoxRecord(ctx context.Context, domain, boxName string, shard int) error {
	return nil
}

func (Discard) DeleteBoxRecord(ctx context.Context, domain, boxName string, shard int) error {
	return nil
}
