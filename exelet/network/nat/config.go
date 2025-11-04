package nat

import "context"

func (n *NAT) Config(ctx context.Context) any {
	return &Config{
		Bridge:  n.bridgeName,
		Network: n.network,
		Router:  n.router,
	}
}
