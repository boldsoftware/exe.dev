package client

type ClientOpt func(c *ClientConfig)

// WithInsecure is an opt that sets insecure for the client
func WithInsecure() ClientOpt {
	return func(c *ClientConfig) {
		c.Insecure = true
	}
}
