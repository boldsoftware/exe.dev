// Package exeprox is the core of the exeprox program.
package exeprox

import (
	"context"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"exe.dev/exeweb"
	proxyapi "exe.dev/pkg/api/exe/proxy/v1"
	"exe.dev/sshpool2"
	"exe.dev/stage"
	"exe.dev/templates"
	"exe.dev/tracing"

	grpcprom "github.com/grpc-ecosystem/go-grpc-middleware/providers/prometheus"
	grpclogging "github.com/grpc-ecosystem/go-grpc-middleware/v2/interceptors/logging"
	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// ProxyConfig is the exeprox configuration details.
type ProxyConfig struct {
	Env             *stage.Env
	Logger          *slog.Logger
	ExedHTTPPort    int           // exed HTTP port, 0 if none
	ExedHTTPSPort   int           // exed HTTPS port, 0 if none
	ExedGRPCAddr    string        // exed GRPC address, empty to disable
	GRPCLatency     time.Duration // artificial latency on gRPC calls, 0 to disable
	HTTPAddr        string        // address on which to serve HTTP
	HTTPSAddr       string        // address on which to serve HTTPS
	CertCacheDir    string        // directory for cached TLS certs, empty to disable
	MetricsRegistry *prometheus.Registry
}

// Proxy is the proxy server.
type Proxy struct {
	grpcClient  proxyapi.ProxyInfoServiceClient
	exeproxData ExeproxData // source for data needed to proxy requests

	boxes      boxesData
	cookies    cookiesData
	users      usersData
	sshKeys    sshKeysData
	exe1Tokens exe1TokensData

	web WebProxy

	sshPool *sshpool2.Pool

	metricsRegistry *prometheus.Registry

	stopping atomic.Bool // reports whether stop was called

	lg *slog.Logger
}

// NewProxy creates a new proxy instance.
func NewProxy(cfg *ProxyConfig) (*Proxy, error) {
	var httpLn *listener
	var err error
	if cfg.HTTPAddr != "" {
		httpLn, err = startListener(cfg.Logger, "http", cfg.HTTPAddr)
		if err != nil {
			return nil, fmt.Errorf("failed to listen on HTTP address %q: %w", cfg.HTTPAddr, err)
		}
		cfg.Logger.Info("http server listening", "addr", httpLn.addr(), "port", httpLn.port())
	}

	var httpsLn *listener
	if cfg.HTTPSAddr != "" {
		httpsLn, err = startListener(cfg.Logger, "https", cfg.HTTPSAddr)
		if err != nil {
			return nil, fmt.Errorf("failed to listen on HTTPS address %q: %w", cfg.HTTPSAddr, err)
		}
		cfg.Logger.Info("https server listening", "addr", httpsLn.addr(), "port", httpsLn.port())
	}

	var grpcClient proxyapi.ProxyInfoServiceClient
	if cfg.ExedGRPCAddr != "" {
		grpcClient, err = startGRPCClient(cfg.Logger, cfg.ExedGRPCAddr, cfg.MetricsRegistry, cfg.GRPCLatency)
		if err != nil {
			return nil, err
		}
	}

	httpMetrics := exeweb.NewHTTPMetrics(cfg.MetricsRegistry)

	tmpls, err := templates.Parse()
	if err != nil {
		return nil, err
	}

	lg := cfg.Logger
	if lg == nil {
		lg = slog.Default()
	}

	sshPool := &sshpool2.Pool{
		TTL:     10 * time.Minute,
		Metrics: sshpool2.NewMetrics(cfg.MetricsRegistry),
	}

	var cc *certCache
	if cfg.CertCacheDir != "" && cfg.HTTPSAddr != "" {
		var err error
		cc, err = newCertCache(cfg.CertCacheDir, lg)
		if err != nil {
			return nil, err
		}
	}

	tc := exeweb.NewTransportCache(5 * time.Minute)
	sshPool.OnConnClosed = func(host string, user string, port int, publicKey string) {
		tc.CloseIdleConnectionsFor(host, user, port, publicKey)
	}

	p := &Proxy{
		grpcClient:      grpcClient,
		lg:              lg,
		sshPool:         sshPool,
		metricsRegistry: cfg.MetricsRegistry,
		web: WebProxy{
			env:            cfg.Env,
			exedHTTPPort:   cfg.ExedHTTPPort,
			exedHTTPSPort:  cfg.ExedHTTPSPort,
			httpLn:         httpLn,
			httpsLn:        httpsLn,
			httpMetrics:    httpMetrics,
			netHTTPLogger:  log.New(httpLogger{cfg.Logger}, "", 0),
			templates:      tmpls,
			transportCache: tc,
			certCache:      cc,
		},
	}
	p.exeproxData = newGRPCExeproxData(grpcClient, lg, &p.boxes, &p.users)
	p.web.proxy = p

	httpMetrics.SetHostFuncs(p.web.isProxyRequest, p.web.boxFromHost)

	p.web.setup()

	return p, nil
}

// Start starts the various servers and keeps them running.
// This method does not return until something has told exeprox to stop.
func (p *Proxy) Start() error {
	if p.stopping.Load() {
		return errors.New("Proxy: invalid Start after Stop")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go p.watchChanges()

	if err := p.web.start(ctx, cancel); err != nil {
		return err
	}

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Tell NewProxy that the server is ready.
	p.lg.InfoContext(ctx, "server started")

	select {
	case sig := <-sigChan:
		p.lg.InfoContext(ctx, "Shutting down exeprox due to signal", "signal", sig)
		p.Stop()
		return nil

	case <-ctx.Done():
		p.lg.ErrorContext(ctx, "Server startup failed, shutting down")
		p.Stop()
		return errors.New("server startup failed")
	}
}

// Stop shuts down all servers.
func (p *Proxy) Stop() {
	p.stopping.Store(true)

	ctx := context.Background()

	p.web.stop(ctx)

	p.lg.DebugContext(ctx, "exeprox servers stopped")
}

// listener is a listening TCP port, with address information.
type listener struct {
	origAddr string       // original requested listening address
	ln       net.Listener // listener
}

// startListener sets up a listener on a given address.
// The typ parameter is just for logging.
func startListener(lg *slog.Logger, typ, addr string) (*listener, error) {
	if addr == "" {
		return nil, fmt.Errorf("listening address for %s is empty", typ)
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("listening on address %s for %s failed: %v", addr, typ, err)
	}

	// Don't log proxy ports, as there are many of them.
	if !strings.HasPrefix(typ, "proxy-") {
		tcpAddr := ln.Addr().(*net.TCPAddr)
		lg.Info("listening", "type", typ, "addr", tcpAddr.String(), "port", tcpAddr.Port)
	}

	ret := &listener{
		origAddr: addr,
		ln:       ln,
	}
	return ret, nil
}

// String returns a user friend version of ln.
func (ln *listener) String() string {
	if ln == nil {
		return "<nil>"
	}
	prefix := ""
	var addr *net.TCPAddr
	if ln.ln == nil {
		prefix = "un"
	} else {
		addr = ln.ln.Addr().(*net.TCPAddr)
	}
	return fmt.Sprintf("<tcp %sstarted addr=%q orig=%q>", prefix, addr, ln.origAddr)
}

// port returns the TCP port on which ln is listening.
func (ln *listener) port() int {
	return ln.ln.Addr().(*net.TCPAddr).Port
}

// addr returns the address on which ln is listening.
func (ln *listener) addr() string {
	return ln.ln.Addr().(*net.TCPAddr).String()
}

// startGRPCClient starts a grpc client to contact exed.
func startGRPCClient(lg *slog.Logger, addr string, metricsRegistry *prometheus.Registry, latency time.Duration) (proxyapi.ProxyInfoServiceClient, error) {
	loggerFunc := func(ctx context.Context, lvl grpclogging.Level, msg string, fields ...any) {
		level := slog.Level(lvl)

		// Downgrade canceled context from error to info.
		// We have to look at the error string,
		// as the grpc middlewarn doesn't pass the error value.
		if level == slog.LevelError {
			for i := 0; i < len(fields); i += 2 {
				if fields[i] == "grpc.error" && i+1 < len(fields) {
					if s, ok := fields[i+1].(string); ok && strings.Contains(s, "context canceled") {
						level = slog.LevelInfo
					}
				}
			}
		}

		lg.Log(ctx, level, msg, fields...)
	}

	clientMetrics := grpcprom.NewClientMetrics(
		grpcprom.WithClientHandlingTimeHistogram(
			grpcprom.WithHistogramBuckets([]float64{0.01, 0.1, 0.3, 0.6, 1, 1.4, 2, 3, 6, 9, 20, 30, 60, 90}),
		),
	)
	metricsRegistry.MustRegister(clientMetrics)

	unaryInterceptors := []grpc.UnaryClientInterceptor{
		tracing.UnaryClientInterceptor(),
		clientMetrics.UnaryClientInterceptor(),
		grpclogging.UnaryClientInterceptor(grpclogging.LoggerFunc(loggerFunc)),
	}
	streamInterceptors := []grpc.StreamClientInterceptor{
		tracing.StreamClientInterceptor(),
		clientMetrics.StreamClientInterceptor(),
		grpclogging.StreamClientInterceptor(grpclogging.LoggerFunc(loggerFunc)),
	}

	if latency > 0 {
		lg.Info("artificial gRPC latency enabled", "latency", latency)
		unaryInterceptors = append(unaryInterceptors, func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
			time.Sleep(latency)
			return invoker(ctx, method, req, reply, cc, opts...)
		})
		streamInterceptors = append(streamInterceptors, func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
			time.Sleep(latency)
			return streamer(ctx, desc, cc, method, opts...)
		})
	}

	opts := []grpc.DialOption{
		// We rely on Tailscale for security.
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		// Wait to reconnect on connection failure.
		grpc.WithDefaultCallOptions(grpc.WaitForReady(true)),
		grpc.WithChainUnaryInterceptor(unaryInterceptors...),
		grpc.WithChainStreamInterceptor(streamInterceptors...),
	}

	u, err := url.Parse(addr)
	if err != nil {
		return nil, fmt.Errorf("failed to parse grpc address %s: %v", addr, err)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("malformed exed grpc address %q", addr)
	}

	lg.Debug("connecting to exed grpc server", "addr", addr, "server", u.Host)

	c, err := grpc.NewClient(u.Host, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to start grpc client: %v", err)
	}

	return proxyapi.NewProxyInfoServiceClient(c), nil
}
