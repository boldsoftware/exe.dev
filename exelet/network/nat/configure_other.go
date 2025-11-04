//go:build !linux

package nat

import (
	"context"
)

func (n *NAT) configureBridge(_ context.Context) error {
	return ErrNotImplemented
}
