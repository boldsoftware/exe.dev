package helpers

import (
	"github.com/urfave/cli/v2"

	"exe.dev/exelet/client"
)

// GetClient returns a exelet client using the specified cli.Context
func GetClient(clix *cli.Context) (*client.Client, error) {
	addr := clix.String("addr")

	return client.NewClient(addr,
		client.WithInsecure(),
	)
}
