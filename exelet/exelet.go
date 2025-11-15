package exelet

import (
	"crypto/tls"
	"log/slog"
	"os"
	"runtime"
	"runtime/pprof"
	"sync"
	"time"

	grpcmiddleware "github.com/grpc-ecosystem/go-grpc-middleware"
	"github.com/pkg/errors"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"exe.dev/exelet/config"
	"exe.dev/exelet/services"
	api "exe.dev/pkg/api/exe/compute/v1"
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
}

// NewExelet returns a new exelet server.
func NewExelet(cfg *config.ExeletConfig, log *slog.Logger, opts ...ServerOpt) (*Exelet, error) {
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
	metricsRegistry := prometheus.NewRegistry()
	metrics := NewExeletMetrics(metricsRegistry)

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
	}

	grpcOpts, err := getGRPCOptions(cfg)
	if err != nil {
		return nil, err
	}

	// middleware
	unaryServerInterceptors := []grpc.UnaryServerInterceptor{
		tracing.UnaryServerInterceptor(),
	}
	streamServerInterceptors := []grpc.StreamServerInterceptor{
		tracing.StreamServerInterceptor(),
	}

	// TODO: auth middleware

	grpcOpts = append(grpcOpts,
		grpc.UnaryInterceptor(grpcmiddleware.ChainUnaryServer(unaryServerInterceptors...)),
		grpc.StreamInterceptor(grpcmiddleware.ChainStreamServer(streamServerInterceptors...)),
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
