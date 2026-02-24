package testinfra

import (
	"context"
	"fmt"
	"io"
	"log/slog"
)

// ServerEnv describes the various servers running for an end-to-end test.
type ServerEnv struct {
	Exed          *ExedInstance
	Exelets       []*ExeletInstance
	Exeprox       *ExeproxInstance
	SSHProxy      *TCPProxy
	ExedHTTPProxy *TCPProxy
	SSHPiperd     *SSHPiperdInstance
	Email         *EmailServer
	Metricsd      *MetricsdInstance
}

// StartServers takes a list of exelets that have already been started,
// and starts all the servers needed for an end-to-end test.
//
// exedHTTPProxy is the proxy for the exed server,
// which must be passed to the exelets.
//
// exedLog, exeproxLog, and piperLog, if not nil,
// are log files for exed, exeprox, and sshpiper.
//
// logPorts is whether to log port numbers using slog.InfoContext.
//
// verboseEmailServer is whether email server should be verbose.
//
// metricsd, if not nil, is a metricsd instance to include in the environment.
func StartServers(ctx context.Context, exelets []*ExeletInstance, exedHTTPProxy *TCPProxy, exedLog, exeproxLog, piperLog io.Writer, logPorts, verboseEmailServer bool, metricsd *MetricsdInstance) (*ServerEnv, error) {
	env := &ServerEnv{
		Exelets:       exelets,
		ExedHTTPProxy: exedHTTPProxy,
		Metricsd:      metricsd,
	}

	// We have a circular dependency around ports.
	// (This is not a problem in production,
	// because we use fixed port numbers.)
	//
	// We need to start exed, which needs to know
	// what port sshpiper is listening on,
	// in order to give correct port numbers out to clients.
	//
	// We need to start sshpiper,
	// which needs to know what exed's piper plugin port is.
	//
	// To work around this, we start a simple TCP proxy first,
	// which will act as the sshpiper port.
	// We then forward traffic from the proxy to
	// the actual sshpiper instance.
	// TODO: figure out why we're seeing connections
	// before SetDestPort is called, and stop doing that.
	sshProxy, err := NewTCPProxy("sshProxy")
	if err != nil {
		return env, fmt.Errorf("failed to create ssh proxy: %w", err)
	}
	go sshProxy.Serve(ctx)
	env.SSHProxy = sshProxy

	if logPorts {
		slog.InfoContext(ctx, "ssh proxy listening", "port", sshProxy.Port())
	}

	// Start email server first so we can pass its URL to exed.
	es, err := StartEmailServer(ctx, verboseEmailServer)
	if err != nil {
		return env, err
	}
	env.Email = es
	if logPorts {
		slog.InfoContext(ctx, "email server listening", "port", es.Port)
	}

	// TODO: build piperd concurrently with
	// starting exed for faster startup.

	var exeletAddrs []string
	for _, exelet := range exelets {
		exeletAddrs = append(exeletAddrs, exelet.Address)
	}

	// Pass "0,0" to let the proxy listeners allocate
	// their own port numbers.
	ei, err := StartExed(ctx, es.Port, sshProxy.Port(), []int{0, 0}, exeletAddrs, exedLog, logPorts)
	if err != nil {
		return env, err
	}
	env.Exed = ei

	epi, err := StartExeprox(ctx, ei.HTTPPort, ei.ExeproxPort, []int{0, 0}, exeproxLog, logPorts)
	if err != nil {
		return env, err
	}
	env.Exeprox = epi

	pi, err := StartSSHPiperd(ctx, ei.PiperPluginPort, piperLog)
	if err != nil {
		return env, err
	}
	env.SSHPiperd = pi
	if logPorts {
		slog.InfoContext(ctx, "sshpiperd listening", "port", pi.Port)
	}

	// Proxy SSH requests to piperd.
	env.SSHProxy.SetDestPort(pi.Port)

	// Now that exed is running,
	// point the HTTP proxy to the real exed HTTP port.
	env.ExedHTTPProxy.SetDestPort(ei.HTTPPort)

	AddCanonicalization(env.SSHProxy.Port(), "SSH_PORT")

	return env, nil
}

// Stop stops all the servers.
// This just logs any errors, it doesn't return them.
//
// This returns a list of local directory containing remote coverage files,
// if any.
func (env *ServerEnv) Stop(ctx context.Context, testRunID string) []string {
	if env.SSHProxy != nil {
		env.SSHProxy.Close()
	}
	if env.ExedHTTPProxy != nil {
		env.ExedHTTPProxy.Close()
	}

	if env.Exed != nil {
		env.Exed.Stop(ctx, testRunID, false)
	}

	if env.Exeprox != nil {
		env.Exeprox.Stop(ctx)
	}

	if env.SSHPiperd != nil {
		env.SSHPiperd.Stop(ctx)
	}

	var coverDirs []string
	for _, exelet := range env.Exelets {
		coverDir := exelet.Stop(ctx)
		if coverDir != "" {
			coverDirs = append(coverDirs, coverDir)
		}
	}

	if env.Metricsd != nil {
		env.Metricsd.Stop(ctx)
	}

	return coverDirs
}

// MetricsdURL returns the URL of the metricsd server, or empty string if not running.
func (env *ServerEnv) MetricsdURL() string {
	if env.Metricsd == nil {
		return ""
	}
	return env.Metricsd.Address
}
