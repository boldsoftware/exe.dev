//go:build !linux

package nat

import (
	"context"
)

func (n *NAT) DeleteInterface(ctx context.Context, id, ip string) error {
	return ErrNotImplemented
}
