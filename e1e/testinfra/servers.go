package testinfra

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"

	"golang.org/x/sync/errgroup"
)

func startTCPProxy(ctx context.Context, name string) (*TCPProxy, error) {
	p, err := NewTCPProxy(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("failed to create %s: %w", name, err)
	}
	go p.Serve()
	return p, nil
}

// ServerEnv describes the various servers running for an end-to-end test.
type ServerEnv struct {
	Exed                 *ExedInstance
	Exelets              []*ExeletInstance
	Exeprox              *ExeproxInstance
	Exepipe              *ExepipeInstance
	SSHProxy             *TCPProxy
	ExedHTTPProxy        *TCPProxy
	ExedPiperPluginProxy *TCPProxy
	ExedExeproxProxy     *TCPProxy
	TCPProxies           []*TCPProxy
	SSHPiperd            *SSHPiperdInstance
	Email                *EmailServer
	Metricsd             *MetricsdInstance
	GitHubMock           *MockGitHubServer
	AnthropicMock        *MockAnthropicServer
}

// PrebuiltBinaries holds paths to pre-built binaries for the servers.
// Exelet is intentionally separate: its cleanup is caller-managed,
// while these binaries use AddCleanup.
type PrebuiltBinaries struct {
	Exed      string
	Exeprox   string
	SSHPiperd string
}

// BuildAll builds all server binaries (exed, exeprox, sshpiperd)
// and the exelet binary concurrently.
// The returned exeletBinary path should be cleaned up by the caller.
func BuildAll(ctx context.Context, testRunID string) (bins PrebuiltBinaries, exeletBinary string, err error) {
	slog.InfoContext(ctx, "building all binaries concurrently")
	g, ctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		var err error
		exeletBinary, err = BuildExeletBinary(ctx, testRunID)
		return err
	})
	g.Go(func() error {
		var err error
		bins.Exed, err = BuildExed(ctx)
		return err
	})
	g.Go(func() error {
		var err error
		bins.Exeprox, err = BuildExeprox(ctx)
		return err
	})
	g.Go(func() error {
		var err error
		bins.SSHPiperd, err = BuildSSHPiperd(ctx)
		return err
	})
	if err := g.Wait(); err != nil {
		return bins, "", err
	}
	slog.InfoContext(ctx, "all binaries built")
	return bins, exeletBinary, nil
}

// StartServers takes a list of exelets that have already been started,
// and starts all the servers needed for an end-to-end test.
//
// bins holds pre-built binary paths for exed, exeprox, and sshpiperd.
//
// tcpProxies is TCP proxies set up beforehand.
// This is just a convenience for closing them.
//
// exedLog, exeproxLog, and piperLog, if not nil,
// are log files for exed, exeprox, and sshpiper.
//
// logPorts is whether to log port numbers using slog.InfoContext.
//
// verboseEmailServer is whether email server should be verbose.
//
// metricsd, if not nil, is a metricsd instance to include in the environment.
func StartServers(ctx context.Context, bins PrebuiltBinaries, exelets []*ExeletInstance, exepipe *ExepipeInstance, tcpProxies []*TCPProxy, exedLog, exeproxLog, piperLog io.Writer, logPorts, verboseEmailServer bool, metricsd *MetricsdInstance) (*ServerEnv, error) {
	env := &ServerEnv{
		Exelets:    exelets,
		Exepipe:    exepipe,
		TCPProxies: tcpProxies,
		Metricsd:   metricsd,
	}

	// We have circular dependencies around ports.
	// (This is not a problem in production,
	// because we use fixed port numbers.)
	//
	// We use TCP proxies to break the cycles:
	// services connect to stable proxy ports,
	// and we retarget the proxies once actual ports are known.
	// On exed restart, the proxies are retargeted to the new ports
	// so that sshpiperd, exeprox, and exelets never see exed's actual ports.

	sshProxy, err := startTCPProxy(ctx, "sshProxy")
	if err != nil {
		return env, err
	}
	env.SSHProxy = sshProxy

	if logPorts {
		slog.InfoContext(ctx, "ssh proxy listening", "port", sshProxy.Port())
	}

	// Adopt "exedHTTPProxy" from tcpProxies.
	var exedHTTPProxy *TCPProxy
	for _, tp := range tcpProxies {
		if tp.Name == "exedHTTPProxy" {
			exedHTTPProxy = tp
			break
		}
	}
	if exedHTTPProxy == nil {
		return env, fmt.Errorf("exedHTTPProxy not found in tcpProxies")
	}
	env.ExedHTTPProxy = exedHTTPProxy

	// Remove adopted proxy from TCPProxies to avoid double-close in Stop.
	filtered := make([]*TCPProxy, 0, len(env.TCPProxies))
	for _, tp := range env.TCPProxies {
		if tp != exedHTTPProxy {
			filtered = append(filtered, tp)
		}
	}
	env.TCPProxies = filtered

	exedPiperPluginProxy, err := startTCPProxy(ctx, "exedPiperPluginProxy")
	if err != nil {
		return env, err
	}
	env.ExedPiperPluginProxy = exedPiperPluginProxy

	exedExeproxProxy, err := startTCPProxy(ctx, "exedExeproxProxy")
	if err != nil {
		return env, err
	}
	env.ExedExeproxProxy = exedExeproxProxy

	// Start email server first so we can pass its URL to exed.
	es, err := StartEmailServer(ctx, verboseEmailServer)
	if err != nil {
		return env, err
	}
	env.Email = es
	if logPorts {
		slog.InfoContext(ctx, "email server listening", "port", es.Port)
	}

	// Start mock GitHub server for GitHub App installation tests.
	ghMock := NewMockGitHubServer()
	env.GitHubMock = ghMock
	os.Setenv("EXE_GITHUB_APP_CLIENT_ID", "mock-github-client-id")
	os.Setenv("EXE_GITHUB_APP_CLIENT_SECRET", "mock-github-client-secret")
	os.Setenv("EXE_GITHUB_APP_SLUG", "mock-app")
	os.Setenv("TEST_GITHUB_TOKEN_URL", ghMock.URL()+"/login/oauth/access_token")
	os.Setenv("TEST_GITHUB_API_URL", ghMock.URL())
	os.Setenv("EXE_GITHUB_APP_ID", TestGitHubAppID)
	os.Setenv("EXE_GITHUB_APP_PRIVATE_KEY", TestGitHubAppPrivateKeyPEM)

	// Start mock Anthropic server for prompt command tests.
	anthropicMock := NewMockAnthropicServer()
	env.AnthropicMock = anthropicMock
	os.Setenv("ANTHROPIC_BASE_URL", anthropicMock.URL())

	var exeletAddrs []string
	for _, exelet := range exelets {
		exeletAddrs = append(exeletAddrs, exelet.Address)
	}

	// Start sshpiperd concurrently with exed.
	// SSHPiperd only needs the proxy port, which is already available.
	var pi *SSHPiperdInstance
	g := new(errgroup.Group)
	g.Go(func() error {
		var err error
		pi, err = StartSSHPiperd(ctx, bins.SSHPiperd, exedPiperPluginProxy.Port(), piperLog)
		return err
	})

	// Pass "0,0" to let the proxy listeners allocate
	// their own port numbers.
	var metricsdURL string
	if metricsd != nil {
		metricsdURL = metricsd.Address
	}
	ei, err := StartExed(ctx, bins.Exed, es.Port, sshProxy.Port(), []int{0, 0}, exeletAddrs, exedLog, logPorts, metricsdURL)
	if err != nil {
		return env, err
	}
	env.Exed = ei

	// Point the proxies at exed's actual ports.
	exedHTTPProxy.SetDestPort(ei.HTTPPort)
	exedPiperPluginProxy.SetDestPort(ei.PiperPluginPort)
	exedExeproxProxy.SetDestPort(ei.ExeproxPort)

	// On restart, retarget all three proxies.
	ei.onRestart = func(ei *ExedInstance) {
		exedHTTPProxy.SetDestPort(ei.HTTPPort)
		exedPiperPluginProxy.SetDestPort(ei.PiperPluginPort)
		exedExeproxProxy.SetDestPort(ei.ExeproxPort)
	}

	// Exeprox uses -exed-http-port only for constructing redirect URLs
	// to exed's web host (auth pages, logout, etc.), not for backend
	// communication (which goes over gRPC via the exeprox proxy).
	// Pass exed's actual HTTP port so redirect URLs reach exed directly.
	//
	// Limitation: after exed restarts, the port here becomes stale,
	// so redirect URLs built by exeprox will point to the old port.
	// No current tests exercise auth redirects after restart.
	// We can't use the proxy port here because exed's
	// isRequestOnMainPort rejects Host headers with non-main ports.
	epi, err := StartExeprox(ctx, bins.Exeprox, ei.HTTPPort, exedExeproxProxy.Port(), []int{0, 0}, exeproxLog, logPorts)
	if err != nil {
		return env, err
	}
	env.Exeprox = epi

	// Wait for sshpiperd to finish starting.
	if err := g.Wait(); err != nil {
		return env, err
	}
	env.SSHPiperd = pi
	if logPorts {
		slog.InfoContext(ctx, "sshpiperd listening", "port", pi.Port)
	}

	// Proxy SSH requests to piperd.
	env.SSHProxy.SetDestPort(pi.Port)

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
	if env.ExedPiperPluginProxy != nil {
		env.ExedPiperPluginProxy.Close()
	}
	if env.ExedExeproxProxy != nil {
		env.ExedExeproxProxy.Close()
	}
	for _, tcpProxy := range env.TCPProxies {
		tcpProxy.Close()
	}

	if env.Exed != nil {
		env.Exed.Stop(ctx, testRunID, false)
	}

	if env.Exeprox != nil {
		env.Exeprox.Stop(ctx)
	}

	if env.Exepipe != nil {
		env.Exepipe.Stop(ctx)
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

	if env.GitHubMock != nil {
		env.GitHubMock.Close()
	}
	if env.AnthropicMock != nil {
		env.AnthropicMock.Close()
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
