package client

import (
	"net/url"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	computeapi "exe.dev/pkg/api/exe/compute/v1"
)

// Client is the GRPC client
type Client struct {
	computeapi.ComputeServiceClient
	conn *grpc.ClientConn
	addr string
	cfg  *ClientConfig
}

type ClientConfig struct {
	Namespace string
	Username  string
	Token     string
	Insecure  bool
}

// NewClient returns a new client configured with the specified address and options
func NewClient(addr string, clientOpts ...ClientOpt) (*Client, error) {
	cfg := &ClientConfig{}
	for _, o := range clientOpts {
		o(cfg)
	}

	opts := getGRPCOptions(cfg)
	c, err := getConn(addr, opts)
	if err != nil {
		return nil, err
	}

	client := &Client{
		computeapi.NewComputeServiceClient(c),
		c,
		addr,
		cfg,
	}

	return client, nil
}

// Conn returns the current configured client connection
func (c *Client) Conn() *grpc.ClientConn {
	return c.conn
}

// Close closes the underlying GRPC client
func (c *Client) Close() error {
	return c.conn.Close()
}

func getConn(addr string, opts []grpc.DialOption) (*grpc.ClientConn, error) {
	u, err := url.Parse(addr)
	if err != nil {
		return nil, err
	}

	endpoint := u.Host

	c, err := grpc.NewClient(endpoint, opts...)
	if err != nil {
		return nil, err
	}

	return c, nil
}

func getGRPCOptions(cfg *ClientConfig) []grpc.DialOption {
	opts := []grpc.DialOption{}

	// TODO: auth

	if cfg.Insecure {
		opts = append(opts,
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		)
	}

	opts = append(opts, grpc.WithDefaultCallOptions(
		grpc.WaitForReady(true),
	))

	return opts
}
