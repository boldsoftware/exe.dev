// Binary exepipe is a simple program that copies data
// between file descriptors. Clients use the package
// exe.dev/exepipe/client to direct exepipe to copy data,
// or to listen to a port and connect it to a TCP address.
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strconv"

	"exe.dev/exepipe"
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
	addr := flag.String("addr", "@exepipe", "Unix domain address on which to listen for commands")
	httpPort := flag.String("http-port", "30304", "HTTP port for metrics, empty for none")
	stageName := flag.String("stage", "prod", `staging env: "prod", "staging", "local", or "test"`)

	flag.Parse()

	env, err := stage.Parse(*stageName)
	if err != nil {
		return err
	}

	metricsRegistry := prometheus.NewRegistry()
	metricsRegistry.MustRegister(prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{}))
	metricsRegistry.MustRegister(prometheus.NewGoCollector())
	version.RegisterBuildInfo(metricsRegistry)

	logging.SetupLogger(env, metricsRegistry, &logging.ResourceAttrs{
		ServiceVersion: version.BuildVersion(),
		DeploymentEnv:  *stageName,
	})

	slog.Info("Starting exepipe server")

	if *httpPort != "" {
		_, err := strconv.Atoi(*httpPort)
		if err != nil {
			return fmt.Errorf("error parsing -http-port flag %q: %v", *httpPort, err)
		}
	}

	unixAddr := &net.UnixAddr{
		Name: *addr,
		Net:  "unixpacket",
	}

	cfg := exepipe.PipeConfig{
		UnixAddr:        unixAddr,
		HTTPPort:        *httpPort,
		Env:             &env,
		Logger:          slog.Default(),
		MetricsRegistry: metricsRegistry,
	}

	pi, err := exepipe.NewPipe(&cfg)
	if err != nil {
		return fmt.Errorf("failed to create exepipe server: %w", err)
	}

	if err := pi.Start(); err != nil {
		return fmt.Errorf("exepipe server error: %w", err)
	}

	return nil
}
