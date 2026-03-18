// Binary exeprox is a reverse proxy that handles data
// coming in to the exelets. We expect multiple exeprox programs
// to handle incoming data, with different exeprox instances
// running in different regions. The various exeprox programs
// will all talk to the single exed program when necessary.
// In the common case they are intended to cache data locally.
package main

import (
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime/pprof"
	"time"

	"exe.dev/exeprox"
	"exe.dev/logging"
	"exe.dev/stage"
	"exe.dev/version"

	"github.com/prometheus/client_golang/prometheus"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	exedHTTPPort := flag.Int("exed-http-port", 80, "exed HTTP port")
	exedHTTPSPort := flag.Int("exed-https-port", 443, "exed HTTPS port")
	exedGRPCAddr := flag.String("exed-grpc-addr", "", "exed GRPC server address for proxies")
	httpAddr := flag.String("http", ":8081", "HTTP server address, empty to disable")
	httpsAddr := flag.String("https", "", "HTTPS server address, empty to disable")
	certCacheDir := flag.String("cert-cache-dir", "exeprox-cert-cache", "directory for cached TLS certificates")
	stageName := flag.String("stage", "prod", `staging env: "prod", "staging", "local", or "test"`)
	grpcLatency := flag.Duration("grpc-latency", 0, "artificial latency on gRPC calls to exed (e.g. 500ms)")
	profilePath := flag.String("profile", "", "Enable CPU profiling for 30 seconds, saving to /tmp/exeprox-profile-<timestamp>.prof or specified path")

	flag.Parse()

	env, err := stage.Parse(*stageName)
	if err != nil {
		return err
	}

	if *exedGRPCAddr == "" {
		return errors.New("-exed-grpc-addr must be specified")
	}

	metricsRegistry := prometheus.NewRegistry()
	metricsRegistry.MustRegister(prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{}))
	metricsRegistry.MustRegister(prometheus.NewGoCollector())
	version.RegisterBuildInfo(metricsRegistry)
	logging.SetupLogger(env, metricsRegistry, &logging.ResourceAttrs{
		ServiceVersion: version.BuildVersion(),
		DeploymentEnv:  *stageName,
	})
	slog.Info("Starting exeprox server")

	var (
		enableProfiling bool
		profPath        string
	)
	if *profilePath != "" {
		enableProfiling = true
		profPath = *profilePath
	} else {
		// Handle -profile without value.
		flag.Visit(func(f *flag.Flag) {
			if f.Name == "profile" {
				enableProfiling = true
			}
		})
	}

	if enableProfiling {
		if profPath == "" {
			profPath = filepath.Join(os.TempDir(), fmt.Sprintf("exeprox-profile-%d.prof", time.Now().Unix()))
		}

		f, err := os.Create(profPath)
		if err != nil {
			return fmt.Errorf("failed to create profile file: %w", err)
		}

		if err := pprof.StartCPUProfile(f); err != nil {
			f.Close()
			return fmt.Errorf("failed to start CPU profile; %w", err)
		}

		go func() {
			time.Sleep(30 * time.Second)
			pprof.StopCPUProfile()
			f.Close()
			slog.Info("CPU profile written", "path", profPath)
		}()

		slog.Info("CPU profiling started for 30 seconds", "path", profPath)
	}

	cfg := exeprox.ProxyConfig{
		Env:             &env,
		Logger:          slog.Default(),
		ExedHTTPPort:    *exedHTTPPort,
		ExedHTTPSPort:   *exedHTTPSPort,
		ExedGRPCAddr:    *exedGRPCAddr,
		GRPCLatency:     *grpcLatency,
		HTTPAddr:        *httpAddr,
		HTTPSAddr:       *httpsAddr,
		CertCacheDir:    *certCacheDir,
		MetricsRegistry: metricsRegistry,
	}

	p, err := exeprox.NewProxy(&cfg)
	if err != nil {
		return fmt.Errorf("failed to create exeprox server: %w", err)
	}

	if err := p.Start(); err != nil {
		return fmt.Errorf("exeprox server error: %w", err)
	}

	return nil
}
