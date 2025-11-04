//go:build !linux

package nat

import (
	"context"
)

func (n *NAT) Start(ctx context.Context) error {
	return ErrNotImplemented
}
