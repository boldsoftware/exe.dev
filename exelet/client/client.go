package client

import (
	"context"
	"log/slog"
	"net/url"

	grpcprom "github.com/grpc-ecosystem/go-grpc-middleware/providers/prometheus"
	"github.com/grpc-ecosystem/go-grpc-middleware/v2/interceptors/logging"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	computeapi "exe.dev/pkg/api/exe/compute/v1"
	storageapi "exe.dev/pkg/api/exe/storage/v1"
	"exe.dev/tracing"
)

// Client is the GRPC client
type Client struct {
	computeapi.ComputeServiceClient
	storageapi.StorageServiceClient
	conn    *grpc.ClientConn
	addr    string
	cfg     *ClientConfig
	arch    string
	version string
}

type ClientConfig struct {
	Namespace string
	Username  string
	Token     string
	Insecure  bool
	Logger    *slog.Logger
	Metrics   *grpcprom.ClientMetrics
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
		ComputeServiceClient: computeapi.NewComputeServiceClient(c),
		StorageServiceClient: storageapi.NewStorageServiceClient(c),
		conn:                 c,
		addr:                 addr,
		cfg:                  cfg,
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

// Arch returns the cached architecture of the exelet server.
// Returns empty string if GetSystemInfo has not been called yet.
func (c *Client) Arch() string {
	return c.arch
}

// Version returns the cached version of the exelet server.
// Returns empty string if GetSystemInfo has not been called yet.
func (c *Client) Version() string {
	return c.version
}

// GetSystemInfo fetches system information from the exelet server and caches it.
func (c *Client) GetSystemInfo(ctx context.Context, req *computeapi.GetSystemInfoRequest) (*computeapi.GetSystemInfoResponse, error) {
	resp, err := c.ComputeServiceClient.GetSystemInfo(ctx, req)
	if err != nil {
		return nil, err
	}
	c.arch = resp.Arch
	c.version = resp.Version
	return resp, nil
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

	// Add trace_id propagation interceptors
	unaryInterceptors := []grpc.UnaryClientInterceptor{
		tracing.UnaryClientInterceptor(),
	}
	streamInterceptors := []grpc.StreamClientInterceptor{
		tracing.StreamClientInterceptor(),
	}

	// Add metrics and logging interceptors if provided
	if cfg.Metrics != nil {
		unaryInterceptors = append(unaryInterceptors, cfg.Metrics.UnaryClientInterceptor())
		streamInterceptors = append(streamInterceptors, cfg.Metrics.StreamClientInterceptor())
	}

	if cfg.Logger != nil {
		loggerFunc := func(ctx context.Context, lvl logging.Level, msg string, fields ...any) {
			cfg.Logger.Log(ctx, slog.Level(lvl), msg, fields...)
		}
		unaryInterceptors = append(unaryInterceptors, logging.UnaryClientInterceptor(logging.LoggerFunc(loggerFunc)))
		streamInterceptors = append(streamInterceptors, logging.StreamClientInterceptor(logging.LoggerFunc(loggerFunc)))
	}

	opts = append(opts,
		grpc.WithChainUnaryInterceptor(unaryInterceptors...),
		grpc.WithChainStreamInterceptor(streamInterceptors...),
	)

	return opts
}
