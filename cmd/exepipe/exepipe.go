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
	"os/exec"
	"strconv"
	"strings"
	"syscall"

	"exe.dev/exepipe"
	"exe.dev/logging"
	"exe.dev/stage"
	"exe.dev/version"

	"github.com/prometheus/client_golang/prometheus"
)

func main() {
	if err := run(); err != nil {
		slog.Error("exepipe failed to start", "error", err)
		os.Exit(1)
	}
}

func run() error {
	addr := flag.String("addr", "@exepipe", "Unix domain address on which to listen for commands")
	httpPort := flag.String("http-port", "30304", "HTTP port for metrics, empty for none")
	stageName := flag.String("stage", "prod", `staging env: "prod", "staging", "local", or "test"`)
	netnsMode := flag.Bool("netns", false, "enable network namespace-aware dialing")
	controller := flag.Bool("controller", false, "run as a controller exepipe")

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

	lg := slog.With("pid", os.Getpid())

	if *controller {
		return runAsController(lg)
	}

	lg.Info("Starting exepipe server")

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
		Logger:          lg,
		MetricsRegistry: metricsRegistry,
	}
	if *netnsMode {
		cfg.DialFunc = exepipe.NetnsDialFunc()
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

// runAsController is used when exepipe is run by systemd.
// It starts the real exepipe as a child process and simply
// waits for it to exit.
//
// We do things this way because we want exepipe to keep
// running until all of its connections have completed.
// If a new exepipe starts up, it will pick up the listeners
// from the old exepipe, but there is no way to transfer the
// active connections (normally sitting in the splice system call).
// We don't want systemd to kill off the old exepipe which will
// kill off the old connections. Instead systemd kills the
// controller process, and leaves the child alone.
func runAsController(lg *slog.Logger) error {
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("os.Executable failed: %v", err)
	}

	args := make([]string, 0, len(os.Args))
	for _, arg := range os.Args[1:] {
		if len(arg) > 0 && arg[0] == '-' {
			flag, _, _ := strings.Cut(strings.TrimLeft(arg, "-"), "=")
			if flag == "controller" {
				continue
			}
		}
		args = append(args, arg)
	}
	cmd := exec.Command(executable, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid: true,
	}
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	lg.Info("starting child exepipe", "args", args)
	if err := cmd.Start(); err != nil {
		return err
	}

	if err := cmd.Wait(); err != nil {
		lg.Warn("child exepipe exited with non-zero status", "error", err)
		// The fact that the child failed doesn't mean that
		// the controller process should fail.
		// Since we expect -controller to be used by systemd,
		// we expect systemd will restart us anyhow.
	}

	lg.Info("exepipe controller exiting")

	return nil
}
