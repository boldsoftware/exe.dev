package helpers

import (
	"cmp"
	"fmt"
	"os"

	"exe.dev/exelet/client"
	"exe.dev/exelet/config"
)

// GetClient returns a exelet client using the specified cli.Context
func GetClient() (*client.Client, error) {
	addr := cmp.Or(os.Getenv(config.EnvVarExeletServerAddress), config.DefaultExeletAddress)

	if addr == "" {
		return nil, fmt.Errorf("addr is needed for the test client")
	}

	return client.NewClient(addr,
		client.WithInsecure(),
	)
}
