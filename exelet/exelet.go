package exelet

import (
	"context"
	"crypto/tls"
	"log/slog"
	"os"
	"runtime"
	"runtime/pprof"
	"sync"
	"time"

	grpcprom "github.com/grpc-ecosystem/go-grpc-middleware/providers/prometheus"
	"github.com/grpc-ecosystem/go-grpc-middleware/v2/interceptors/logging"
	"github.com/pkg/errors"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/reflection"

	"exe.dev/exelet/config"
	"exe.dev/exelet/services"
	exelogging "exe.dev/logging"
	api "exe.dev/pkg/api/exe/compute/v1"
	"exe.dev/stage"
	"exe.dev/tracing"
)

var (
	// ErrServiceRegistered is returned if an existing service is already registered for the specified type
	ErrServiceRegistered = errors.New("service is already registered for the specified type")

	updateInterval = time.Second * 10
)

// Exelet is the exelet server.
type Exelet struct {
	started         time.Time
	config          *config.ExeletConfig
	mu              *sync.Mutex
	log             *slog.Logger
	grpcServer      *grpc.Server
	serverCloseCh   chan struct{}
	updateTicker    *time.Ticker
	services        []services.Service
	state           api.Server_ServerState
	metricsRegistry *prometheus.Registry
	metrics         *ExeletMetrics
	grpcMetrics     *grpcprom.ServerMetrics
	slackFeed       *exelogging.SlackFeed
}

// NewExelet returns a new exelet server.
func NewExelet(cfg *config.ExeletConfig, log *slog.Logger, env stage.Env, opts ...ServerOpt) (*Exelet, error) {
	state := api.Server_INIT

	// apply opts
	optCfg := &OptConfig{}
	for _, o := range opts {
		o(optCfg)
	}
	if optCfg.IsMaintenance {
		state = api.Server_MAINTENANCE
		log.Info("starting server in maintenance mode")
	}

	log.Info("starting exelet server", "addr", cfg.ListenAddress)

	// create prometheus registry and metrics
	metricsRegistry := optCfg.MetricsRegistry
	metrics := NewExeletMetrics(metricsRegistry)

	// Create gRPC server metrics
	grpcMetrics := grpcprom.NewServerMetrics(
		grpcprom.WithServerHandlingTimeHistogram(
			grpcprom.WithHistogramBuckets([]float64{0.01, 0.1, 0.3, 0.6, 1, 1.4, 2, 3, 6, 9, 20, 30, 60, 90}),
		),
	)
	metricsRegistry.MustRegister(grpcMetrics)

	srv := &Exelet{
		started:         time.Now(),
		config:          cfg,
		mu:              &sync.Mutex{},
		log:             log,
		serverCloseCh:   make(chan struct{}),
		updateTicker:    time.NewTicker(updateInterval),
		state:           state,
		metricsRegistry: metricsRegistry,
		metrics:         metrics,
		grpcMetrics:     grpcMetrics,
		slackFeed:       exelogging.NewSlackFeed(log, env),
	}

	grpcOpts, err := getGRPCOptions(cfg)
	if err != nil {
		return nil, err
	}

	// Adapter to convert slog.Logger to logging.Logger
	loggerFunc := func(ctx context.Context, lvl logging.Level, msg string, fields ...any) {
		log.Log(ctx, slog.Level(lvl), msg, fields...)
	}

	// middleware
	unaryServerInterceptors := []grpc.UnaryServerInterceptor{
		tracing.UnaryServerInterceptor(),
		grpcMetrics.UnaryServerInterceptor(),
		logging.UnaryServerInterceptor(logging.LoggerFunc(loggerFunc), logging.WithLogOnEvents(logging.FinishCall)),
	}
	streamServerInterceptors := []grpc.StreamServerInterceptor{
		tracing.StreamServerInterceptor(),
		grpcMetrics.StreamServerInterceptor(),
		logging.StreamServerInterceptor(logging.LoggerFunc(loggerFunc), logging.WithLogOnEvents(logging.FinishCall)),
	}

	// TODO: auth middleware

	grpcOpts = append(grpcOpts,
		grpc.ChainUnaryInterceptor(unaryServerInterceptors...),
		grpc.ChainStreamInterceptor(streamServerInterceptors...),
	)

	grpcServer := grpc.NewServer(grpcOpts...)
	srv.grpcServer = grpcServer

	return srv, nil
}

// Register registers new services with the GRPC server.
func (s *Exelet) Register(ctx *services.ServiceContext, svcs []func(*config.ExeletConfig, *slog.Logger) (services.Service, error)) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// register services from caller
	registered := map[services.Type]struct{}{}
	for _, svc := range svcs {
		i, err := svc(s.config, s.log)
		if err != nil {
			return err
		}
		if err := i.Register(ctx, s.grpcServer); err != nil {
			return err
		}

		// check for existing service
		if _, exists := registered[i.Type()]; exists {
			return errors.Wrap(ErrServiceRegistered, string(i.Type()))
		}
		s.log.Info("registered service", "type", i.Type())
		registered[i.Type()] = struct{}{}
		s.services = append(s.services, i)
	}

	// Initialize metrics after all services are registered
	s.grpcMetrics.InitializeMetrics(s.grpcServer)

	// Enable gRPC reflection for service discovery.
	// Use grpcurl to interact with this server: https://github.com/fullstorydev/grpcurl
	// Example: grpcurl -plaintext localhost:9080 list
	reflection.Register(s.grpcServer)

	return nil
}

// GenerateProfile generates a new performance profile.
func (s *Exelet) GenerateProfile() (string, error) {
	tmpfile, err := os.CreateTemp("", "exelet-profile-")
	if err != nil {
		return "", err
	}
	runtime.GC()
	if err := pprof.WriteHeapProfile(tmpfile); err != nil {
		return "", err
	}
	tmpfile.Close()
	return tmpfile.Name(), nil
}

// MetricsRegistry returns the prometheus metrics registry.
func (s *Exelet) MetricsRegistry() *prometheus.Registry {
	return s.metricsRegistry
}

func (s *Exelet) updateState(v api.Server_ServerState) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// check if in maintenace and skip updating as
	// server has been started in MAINTENANCE mode
	if s.state == api.Server_MAINTENANCE {
		return
	}
	s.state = v
}

func getGRPCOptions(cfg *config.ExeletConfig) ([]grpc.ServerOption, error) {
	grpcOpts := []grpc.ServerOption{}
	if cfg.TLSServerCertificate != "" && cfg.TLSServerKey != "" {
		logrus.WithFields(logrus.Fields{
			"cert": cfg.TLSServerCertificate,
			"key":  cfg.TLSServerKey,
		}).Debug("configuring TLS for GRPC")
		cert, err := tls.LoadX509KeyPair(cfg.TLSServerCertificate, cfg.TLSServerKey)
		if err != nil {
			return nil, err
		}
		creds := credentials.NewTLS(&tls.Config{
			Certificates:       []tls.Certificate{cert},
			ClientAuth:         tls.RequestClientCert,
			InsecureSkipVerify: cfg.TLSInsecureSkipVerify,
		})
		grpcOpts = append(grpcOpts, grpc.Creds(creds))
	}
	return grpcOpts, nil
}
