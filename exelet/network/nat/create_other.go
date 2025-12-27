//go:build !linux

package nat

import (
	"context"

	api "exe.dev/pkg/api/exe/compute/v1"
)

func (n *NAT) CreateInterface(ctx context.Context, id string) (*api.NetworkInterface, error) {
	return nil, ErrNotImplemented
}

func (n *NAT) ApplyConnectionLimit(ctx context.Context, ip string) error {
	return ErrNotImplemented
}
