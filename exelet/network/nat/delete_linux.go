//go:build linux

package nat

import "context"

func (n *NAT) DeleteInterface(ctx context.Context, id string) error {
	tapName := getTapID(id)
	if err := n.deleteTapInterface(tapName); err != nil {
		return err
	}
	return nil
}
