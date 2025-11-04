//go:build !linux

package nat

import (
	"context"
)

func (n *NAT) DeleteInterface(ctx context.Context, id string) error {
	return ErrNotImplemented
}
