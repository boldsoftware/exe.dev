//go:build !linux

package nat

import (
	"context"
)

func (n *NAT) Stop(ctx context.Context) error {
	return ErrNotImplemented
}
